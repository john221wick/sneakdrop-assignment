package drop

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const ttl = 5 * time.Minute

func newStore(tb testing.TB, stock int) (*Store, *fakeClock) {
	tb.Helper()
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	return New(Config{Stock: stock, HoldTTL: ttl, Now: clock.Now}), clock
}

func mustHold(t *testing.T, s *Store, user string) Hold {
	t.Helper()
	h, err := s.Hold(user)
	if err != nil {
		t.Fatalf("Hold(%s): %v", user, err)
	}
	return h
}

// checkInvariant fails if a pair is missing or counted twice.
func checkInvariant(t *testing.T, s *Store) Stats {
	t.Helper()
	st := s.Stats()
	if st.Available < 0 || st.Available+st.Held+st.Sold != st.Total {
		t.Fatalf("available %d + held %d + sold %d != total %d", st.Available, st.Held, st.Sold, st.Total)
	}
	return st
}

func TestThousandsOfBuyersGetExactlyTheStock(t *testing.T) {
	s, _ := newStore(t, 20)
	// The race detector allows about 8000 live goroutines, so stay under it.
	const buyers = 5000

	var held atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range buyers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Hold(fmt.Sprintf("u%d", i))
			switch {
			case err == nil:
				held.Add(1)
			case !errors.Is(err, ErrSoldOut):
				t.Errorf("u%d: %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if held.Load() != 20 {
		t.Fatalf("%d holds granted, want 20", held.Load())
	}
	if st := checkInvariant(t, s); st.Available != 0 || st.Held != 20 {
		t.Fatalf("stats %+v", st)
	}
}

func TestOneUserClickingManyTimesGetsOneHold(t *testing.T) {
	s, _ := newStore(t, 20)

	var held atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Hold("ana")
			switch {
			case err == nil:
				held.Add(1)
			case !errors.Is(err, ErrAlreadyHolding):
				t.Errorf("unexpected error %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if held.Load() != 1 {
		t.Fatalf("%d holds for one user, want 1", held.Load())
	}
	if st := checkInvariant(t, s); st.Available != 19 {
		t.Fatalf("available %d, want 19", st.Available)
	}
}

func TestPurchaseLimitIsTwo(t *testing.T) {
	s, _ := newStore(t, 20)
	for i := range PurchaseLimit {
		h := mustHold(t, s, "ana")
		if out := s.ApplyPayment(fmt.Sprintf("evt%d", i), h.ID, PaymentSucceeded); out != OutcomeApplied {
			t.Fatalf("payment %d: %s", i, out)
		}
	}
	if _, err := s.Hold("ana"); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("third hold: %v, want limit_reached", err)
	}
	if st := checkInvariant(t, s); st.Sold != 2 {
		t.Fatalf("sold %d, want 2", st.Sold)
	}
}

func TestUnpaidHoldGoesBackToStock(t *testing.T) {
	s, clock := newStore(t, 1)
	mustHold(t, s, "ana")
	if _, err := s.Hold("ben"); !errors.Is(err, ErrSoldOut) {
		t.Fatalf("ben: %v, want sold_out", err)
	}

	clock.Advance(ttl - time.Millisecond)
	if v := s.View("ana"); v.Hold == nil || v.Hold.ExpiresInMs != 1 || v.HoldTTLMs != ttl.Milliseconds() {
		t.Fatalf("ana should still hold with 1ms left: %+v", v.Hold)
	}

	clock.Advance(time.Millisecond)
	s.Sweep()
	if v := s.View("ana"); v.Hold != nil || v.Available != 1 {
		t.Fatalf("hold should be gone and the pair back: %+v", v)
	}
	mustHold(t, s, "ben")
	checkInvariant(t, s)
}

func TestExpiredHoldGoesToFirstInLine(t *testing.T) {
	s, clock := newStore(t, 2)
	mustHold(t, s, "ana")
	clock.Advance(time.Minute)
	mustHold(t, s, "ben")

	for want, user := range []string{"cat", "dan"} {
		if pos, err := s.JoinLine(user); err != nil || pos != want+1 {
			t.Fatalf("JoinLine(%s) = %d, %v; want %d", user, pos, err, want+1)
		}
	}
	if pos, _ := s.JoinLine("cat"); pos != 1 {
		t.Fatalf("joining again moved cat to %d", pos)
	}

	clock.Advance(4 * time.Minute) // ana's hold runs out, ben's does not
	s.Sweep()

	cat := s.View("cat")
	if cat.Hold == nil || cat.QueuePosition != nil {
		t.Fatalf("cat should hold the pair now: %+v", cat)
	}
	if got := time.Duration(cat.Hold.ExpiresInMs) * time.Millisecond; got != ttl {
		t.Fatalf("cat got %v to pay, want a full %v", got, ttl)
	}
	if dan := s.View("dan"); dan.QueuePosition == nil || *dan.QueuePosition != 1 {
		t.Fatalf("dan should be first in line: %+v", dan.QueuePosition)
	}
	if s.View("ana").Hold != nil {
		t.Fatal("ana still has a hold")
	}
	// The pair went to the line, not back on sale.
	if _, err := s.Hold("eve"); !errors.Is(err, ErrSoldOut) {
		t.Fatalf("eve: %v, want sold_out", err)
	}
	checkInvariant(t, s)
}

func TestJoinLineRules(t *testing.T) {
	s, _ := newStore(t, 1)
	if _, err := s.JoinLine("ana"); !errors.Is(err, ErrStillAvailable) {
		t.Fatalf("join with stock left: %v", err)
	}
	mustHold(t, s, "ana")
	if _, err := s.JoinLine("ana"); !errors.Is(err, ErrAlreadyHolding) {
		t.Fatalf("join while holding: %v", err)
	}
	if pos, err := s.JoinLine("ben"); err != nil || pos != 1 {
		t.Fatalf("ben: %d, %v", pos, err)
	}
}

func TestDuplicatePaymentEventIsIgnored(t *testing.T) {
	s, _ := newStore(t, 20)
	h := mustHold(t, s, "ana")
	if out := s.ApplyPayment("evt1", h.ID, PaymentSucceeded); out != OutcomeApplied {
		t.Fatalf("first: %s", out)
	}
	if out := s.ApplyPayment("evt1", h.ID, PaymentSucceeded); out != OutcomeDuplicate {
		t.Fatalf("repeat: %s", out)
	}
	if v := s.View("ana"); v.Purchases != 1 || v.Sold != 1 || v.RefundsDue != 0 {
		t.Fatalf("view %+v", v)
	}
}

func TestSecondChargeForPaidHoldNeedsRefund(t *testing.T) {
	s, _ := newStore(t, 20)
	h := mustHold(t, s, "ana")
	s.ApplyPayment("evt1", h.ID, PaymentSucceeded)
	if out := s.ApplyPayment("evt2", h.ID, PaymentSucceeded); out != OutcomeRefundRequired {
		t.Fatalf("second charge: %s", out)
	}
	if v := s.View("ana"); v.Purchases != 1 || v.RefundsDue != 1 {
		t.Fatalf("view %+v", v)
	}
}

func TestLatePaymentAfterExpiryNeedsRefund(t *testing.T) {
	s, clock := newStore(t, 1)
	h := mustHold(t, s, "ana")
	s.JoinLine("ben")

	// No Sweep: the payment itself must see that the deadline passed.
	clock.Advance(ttl)
	if out := s.ApplyPayment("evt1", h.ID, PaymentSucceeded); out != OutcomeRefundRequired {
		t.Fatalf("late payment: %s", out)
	}
	if v := s.View("ana"); v.Purchases != 0 || v.RefundsDue != 1 {
		t.Fatalf("ana %+v", v)
	}
	if v := s.View("ben"); v.Hold == nil {
		t.Fatal("ben should have been given the pair")
	}
	checkInvariant(t, s)
}

func TestFailureEventsNeverUndoAPurchase(t *testing.T) {
	s, _ := newStore(t, 20)

	// Failure arriving after the success.
	h := mustHold(t, s, "ana")
	s.ApplyPayment("ok-1", h.ID, PaymentSucceeded)
	if out := s.ApplyPayment("fail-1", h.ID, PaymentFailed); out != OutcomeFailureNoted {
		t.Fatalf("late failure: %s", out)
	}
	if v := s.View("ana"); v.Purchases != 1 {
		t.Fatalf("ana lost her purchase: %+v", v)
	}

	// Failure arriving before the success.
	h = mustHold(t, s, "ben")
	s.ApplyPayment("fail-2", h.ID, PaymentFailed)
	if v := s.View("ben"); v.Hold == nil {
		t.Fatal("a failure should leave the hold in place")
	}
	if out := s.ApplyPayment("ok-2", h.ID, PaymentSucceeded); out != OutcomeApplied {
		t.Fatalf("success after failure: %s", out)
	}
	if st := checkInvariant(t, s); st.Sold != 2 {
		t.Fatalf("sold %d, want 2", st.Sold)
	}
}

func TestConcurrentWebhooksPayOnce(t *testing.T) {
	s, _ := newStore(t, 20)
	h := mustHold(t, s, "ana")

	var applied atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Half are the same success redelivered, half are stray failures.
			var out Outcome
			if i%2 == 0 {
				out = s.ApplyPayment("evt-success", h.ID, PaymentSucceeded)
			} else {
				out = s.ApplyPayment(fmt.Sprintf("evt-fail-%d", i), h.ID, PaymentFailed)
			}
			if out == OutcomeApplied {
				applied.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if applied.Load() != 1 {
		t.Fatalf("applied %d times, want 1", applied.Load())
	}
	if st := checkInvariant(t, s); st.Sold != 1 {
		t.Fatalf("sold %d, want 1", st.Sold)
	}
}

// TestRandomTrafficKeepsEveryRule throws a random mix of every operation at
// the store from many goroutines while time moves on, then checks that no
// rule was broken. Run it with -race.
func TestRandomTrafficKeepsEveryRule(t *testing.T) {
	s, clock := newStore(t, 20)
	stop := make(chan struct{})
	var bg sync.WaitGroup

	bg.Add(1)
	go func() {
		defer bg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				clock.Advance(time.Duration(rand.N(20)) * time.Second)
				s.Sweep()
				checkInvariant(t, s)
			}
		}
	}()

	var wg sync.WaitGroup
	for w := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 400 {
				user := fmt.Sprintf("u%d", rand.N(80))
				switch rand.N(5) {
				case 0, 1:
					s.Hold(user)
				case 2:
					s.JoinLine(user)
				case 3:
					if v := s.View(user); v.Hold != nil {
						// Sometimes reuse an event id to mimic redelivery.
						id := fmt.Sprintf("evt-%d-%d", w, i)
						if rand.N(3) == 0 {
							id = fmt.Sprintf("evt-hold-%d", v.Hold.ID)
						}
						kind := PaymentSucceeded
						if rand.N(4) == 0 {
							kind = PaymentFailed
						}
						s.ApplyPayment(id, v.Hold.ID, kind)
					}
				case 4:
					s.View(user)
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	bg.Wait()

	st := checkInvariant(t, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	for user, n := range s.bought {
		if n > PurchaseLimit {
			t.Errorf("%s bought %d pairs", user, n)
		}
	}
	if st.Sold == 0 {
		t.Error("nothing sold; the test is not exercising payments")
	}
	t.Logf("end state %+v", st)
}

// BenchmarkSoldOutClick is a Buy click after the sell-out. It only reads the
// atomic counter, so parallel clicks never wait for each other.
func BenchmarkSoldOutClick(b *testing.B) {
	s, _ := newStore(b, 20)
	for i := range 20 {
		s.Hold(fmt.Sprintf("u%d", i))
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Hold("late")
		}
	})
}

// BenchmarkLockedCall is a call that takes the mutex, for comparison.
func BenchmarkLockedCall(b *testing.B) {
	s, _ := newStore(b, 20)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.View("someone")
		}
	})
}
