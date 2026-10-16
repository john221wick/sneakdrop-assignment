package payments

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type inbox struct {
	mu     sync.Mutex
	events []Event
	calls  int
}

func (b *inbox) snapshot() ([]Event, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Event(nil), b.events...), b.calls
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChargeSendsSignedDuplicateAndFailureEvents(t *testing.T) {
	secret := []byte("s3cret")
	var box inbox
	shop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !Verify(secret, body, r.Header.Get(SignatureHeader)) {
			t.Errorf("bad signature on %s", body)
		}
		var ev Event
		json.Unmarshal(body, &ev)
		box.mu.Lock()
		box.events = append(box.events, ev)
		box.mu.Unlock()
	}))
	defer shop.Close()

	p := New(shop.URL, secret)
	p.MaxDelay = 0
	p.DuplicateRate = 1
	p.FailureRate = 1

	id := p.Charge(7)
	waitFor(t, func() bool { evs, _ := box.snapshot(); return len(evs) == 3 })

	if again := p.Charge(7); again != id {
		t.Fatalf("second charge for the same hold returned %s, want %s", again, id)
	}
	time.Sleep(50 * time.Millisecond)

	evs, _ := box.snapshot()
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3 (charging twice must not pay twice)", len(evs))
	}
	var successes, failures int
	for _, ev := range evs {
		if ev.HoldID != 7 {
			t.Errorf("event for hold %d", ev.HoldID)
		}
		switch ev.Kind {
		case KindSuccess:
			successes++
			if ev.ID != id {
				t.Errorf("success id %s, want %s", ev.ID, id)
			}
		case KindFailed:
			failures++
		}
	}
	if successes != 2 || failures != 1 {
		t.Fatalf("%d successes, %d failures; want 2 and 1", successes, failures)
	}
}

func TestDeliveryRetriesUntilAccepted(t *testing.T) {
	var box inbox
	shop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		box.mu.Lock()
		defer box.mu.Unlock()
		box.calls++
		if box.calls == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}
	}))
	defer shop.Close()

	p := New(shop.URL, []byte("k"))
	p.MaxDelay = 0
	p.DuplicateRate = 0
	p.FailureRate = 0
	p.Charge(1)

	waitFor(t, func() bool { _, calls := box.snapshot(); return calls == 2 })
}
