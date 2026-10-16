// Package drop holds the rules of the sneaker drop.
//
// All state lives in memory behind one sync.Mutex. Every rule check and the
// change it allows happen while that lock is held, so two requests can never
// both see "1 pair left" and both take it. That race is how the last sale
// sold 51 pairs out of 20.
//
// One atomic counter mirrors the number of pairs left. It is only written
// while the lock is held, but it can be read without the lock. That lets the
// thousands of clicks that arrive after the sell-out be turned away without
// queueing on the mutex.
package drop

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// PurchaseLimit is the most pairs one user can buy in total.
const PurchaseLimit = 2

// Errors returned to callers. The text is the error code the API sends.
var (
	ErrSoldOut        = errors.New("sold_out")
	ErrAlreadyHolding = errors.New("already_holding")
	ErrLimitReached   = errors.New("limit_reached")
	ErrStillAvailable = errors.New("available")
	ErrHoldNotFound   = errors.New("hold_not_found")
	ErrHoldNotActive  = errors.New("hold_not_active")
)

type HoldStatus string

const (
	HoldActive  HoldStatus = "active"
	HoldPaid    HoldStatus = "paid"
	HoldExpired HoldStatus = "expired"
)

// Hold is one pair kept aside for one user until ExpiresAt.
type Hold struct {
	ID        int64      `json:"id"`
	User      string     `json:"user"`
	Status    HoldStatus `json:"status"`
	ExpiresAt time.Time  `json:"expires_at"`
}

// PaymentKind is what a payment webhook says happened.
type PaymentKind string

const (
	PaymentSucceeded PaymentKind = "success"
	PaymentFailed    PaymentKind = "failed"
)

// Outcome is what the store did with one payment event.
type Outcome string

const (
	// The hold became a purchase.
	OutcomeApplied Outcome = "applied"
	// This event id was handled before. Nothing changed.
	OutcomeDuplicate Outcome = "duplicate"
	// Money arrived for a hold that had already expired, or for a hold that
	// another event already paid. The pair is never taken back from whoever
	// has it now; the money is flagged to be returned instead.
	OutcomeRefundRequired Outcome = "refund_required"
	// A failed attempt. It never undoes a purchase, and an active hold stays
	// active so the user can still pay before it runs out.
	OutcomeFailureNoted Outcome = "failure_noted"
	// No hold has this id.
	OutcomeUnknownHold Outcome = "unknown_hold"
)

type Config struct {
	Stock   int
	HoldTTL time.Duration
	// Now returns the current time. Defaults to time.Now; tests pass a fake clock.
	Now func() time.Time
	// OnExpire, if set, is called when a hold runs out, with the hold the pair
	// was passed to, or nil if it went back on sale. It runs with the store
	// locked, so it must be quick and must not call the store.
	OnExpire func(expired Hold, next *Hold)
}

type Store struct {
	ttl      time.Duration
	now      func() time.Time
	onExpire func(expired Hold, next *Hold)

	// left mirrors free. It is written only with mu held and read without it.
	left atomic.Int64

	mu         sync.Mutex
	total      int
	free       int                // pairs on sale: not held and not sold
	sold       int                // pairs paid for
	holds      map[int64]*Hold    // every hold ever made, by id
	active     map[string]*Hold   // each user's one active hold
	bought     map[string]int     // pairs paid for, per user
	refunds    map[string]int     // payments that must be returned, per user
	line       []string           // waiting users, first in line at index 0
	ticket     map[string]int64   // ticket number of each waiting user
	served     int64              // how many tickets have left the front of the line
	events     map[string]Outcome // payment event id -> what we did with it
	nextHoldID int64
}

func New(cfg Config) *Store {
	s := &Store{ttl: cfg.HoldTTL, now: cfg.Now, onExpire: cfg.OnExpire, total: cfg.Stock}
	if s.now == nil {
		s.now = time.Now
	}
	if s.onExpire == nil {
		s.onExpire = func(Hold, *Hold) {}
	}
	s.resetLocked()
	return s
}

// Reset puts every pair back on sale and forgets all users and payments.
// Hold ids keep counting up, so an id is never reused.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
}

func (s *Store) resetLocked() {
	s.sold = 0
	s.holds = map[int64]*Hold{}
	s.active = map[string]*Hold{}
	s.bought = map[string]int{}
	s.refunds = map[string]int{}
	s.line = nil
	s.ticket = map[string]int64{}
	s.served = 0
	s.events = map[string]Outcome{}
	s.setFree(s.total)
}

// Hold keeps one pair aside for user for the hold TTL.
func (s *Store) Hold(user string) (Hold, error) {
	// Fast path: once sold out, the flood of extra clicks is answered from
	// one atomic read and never waits for the lock. The count is checked
	// again under the lock below, so a stale read can't oversell.
	if s.left.Load() == 0 {
		return Hold{}, ErrSoldOut
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()

	if s.bought[user] >= PurchaseLimit {
		return Hold{}, ErrLimitReached
	}
	if s.active[user] != nil {
		return Hold{}, ErrAlreadyHolding
	}
	if s.free == 0 {
		return Hold{}, ErrSoldOut
	}
	s.setFree(s.free - 1)
	return *s.grantLocked(user), nil
}

// JoinLine puts user at the back of the waiting line and returns their
// place, counting from 1. Joining again keeps the place they have.
func (s *Store) JoinLine(user string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()

	if _, waiting := s.ticket[user]; waiting {
		return s.positionLocked(user), nil
	}
	if s.free > 0 {
		return 0, ErrStillAvailable
	}
	if s.active[user] != nil {
		return 0, ErrAlreadyHolding
	}
	if s.bought[user] >= PurchaseLimit {
		return 0, ErrLimitReached
	}
	s.ticket[user] = s.served + int64(len(s.line))
	s.line = append(s.line, user)
	return s.positionLocked(user), nil
}

// CheckPayable reports whether a payment for holdID should be started now.
// If user is not empty, the hold must belong to them.
func (s *Store) CheckPayable(holdID int64, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()

	h := s.holds[holdID]
	if h == nil || (user != "" && h.User != user) {
		return ErrHoldNotFound
	}
	if h.Status != HoldActive {
		return ErrHoldNotActive
	}
	return nil
}

// ApplyPayment handles one message from the payment provider. Messages can
// arrive late, more than once, and in any order, so it is safe to call with
// any of them at any time.
func (s *Store) ApplyPayment(eventID string, holdID int64, kind PaymentKind) Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Expire first, so a success that lands after the deadline is late even
	// if the sweeper has not run yet.
	s.expireLocked()

	if _, seen := s.events[eventID]; seen {
		return OutcomeDuplicate
	}
	out := s.applyPaymentLocked(holdID, kind)
	s.events[eventID] = out
	return out
}

func (s *Store) applyPaymentLocked(holdID int64, kind PaymentKind) Outcome {
	h := s.holds[holdID]
	switch {
	case h == nil:
		return OutcomeUnknownHold
	case kind == PaymentFailed:
		return OutcomeFailureNoted
	case h.Status != HoldActive:
		s.refunds[h.User]++
		return OutcomeRefundRequired
	}
	h.Status = HoldPaid
	delete(s.active, h.User)
	s.bought[h.User]++
	s.sold++
	return OutcomeApplied
}

// Sweep ends every hold whose time is up.
func (s *Store) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
}

// Run calls Sweep on a timer until ctx is done. Every other method also
// expires holds first, so Run only matters when no requests come in: it makes
// sure a waiting user gets their pair promptly anyway.
func (s *Store) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep()
		}
	}
}

// View is what one user sees on the page.
type View struct {
	Available     int       `json:"available"`
	Total         int       `json:"total"`
	Sold          int       `json:"sold"`
	Purchases     int       `json:"purchases"`
	RefundsDue    int       `json:"refunds_due"`
	Hold          *HoldView `json:"hold"`
	QueuePosition *int      `json:"queue_position"`
	QueueLength   int       `json:"queue_length"`
	HoldTTLMs     int64     `json:"hold_ttl_ms"`
}

type HoldView struct {
	ID          int64     `json:"id"`
	ExpiresAt   time.Time `json:"expires_at"`
	ExpiresInMs int64     `json:"expires_in_ms"`
}

func (s *Store) View(user string) View {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()

	v := View{
		Available:   s.free,
		Total:       s.total,
		Sold:        s.sold,
		Purchases:   s.bought[user],
		RefundsDue:  s.refunds[user],
		QueueLength: len(s.line),
		HoldTTLMs:   s.ttl.Milliseconds(),
	}
	if h := s.active[user]; h != nil {
		v.Hold = &HoldView{ID: h.ID, ExpiresAt: h.ExpiresAt, ExpiresInMs: h.ExpiresAt.Sub(s.now()).Milliseconds()}
	}
	if _, waiting := s.ticket[user]; waiting {
		p := s.positionLocked(user)
		v.QueuePosition = &p
	}
	return v
}

// Stats is a whole-drop summary. Available + Held + Sold always equals Total.
type Stats struct {
	Total      int `json:"total"`
	Available  int `json:"available"`
	Held       int `json:"held"`
	Sold       int `json:"sold"`
	Waiting    int `json:"waiting"`
	RefundsDue int `json:"refunds_due"`
}

func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()

	st := Stats{Total: s.total, Available: s.free, Held: len(s.active), Sold: s.sold, Waiting: len(s.line)}
	for _, n := range s.refunds {
		st.RefundsDue += n
	}
	return st
}

// expireLocked ends every hold whose deadline has passed and passes each
// freed pair on. Every method calls it first, so a hold counts as expired the
// moment its deadline passes, not only when the sweeper next runs.
func (s *Store) expireLocked() {
	now := s.now()
	var expired []*Hold
	for _, h := range s.active {
		if !now.Before(h.ExpiresAt) {
			expired = append(expired, h)
		}
	}
	slices.SortFunc(expired, func(a, b *Hold) int { return a.ExpiresAt.Compare(b.ExpiresAt) })

	for _, h := range expired {
		h.Status = HoldExpired
		delete(s.active, h.User)
		var passedTo *Hold
		if next := s.releaseLocked(); next != nil {
			c := *next
			passedTo = &c
		}
		s.onExpire(*h, passedTo)
	}
}

// releaseLocked gives one freed pair to the first user in line, with a fresh
// hold of their own. If nobody is waiting, the pair goes back on sale.
func (s *Store) releaseLocked() *Hold {
	for len(s.line) > 0 {
		user := s.line[0]
		s.line = s.line[1:]
		delete(s.ticket, user)
		s.served++
		// JoinLine already checked these, but check again so the limits hold
		// no matter how the user got here.
		if s.bought[user] >= PurchaseLimit || s.active[user] != nil {
			continue
		}
		return s.grantLocked(user)
	}
	s.setFree(s.free + 1)
	return nil
}

func (s *Store) grantLocked(user string) *Hold {
	s.nextHoldID++
	h := &Hold{ID: s.nextHoldID, User: user, Status: HoldActive, ExpiresAt: s.now().Add(s.ttl)}
	s.holds[h.ID] = h
	s.active[user] = h
	return h
}

// positionLocked works in O(1): users only ever leave the line from the
// front, so a place is the user's ticket minus the tickets already served.
func (s *Store) positionLocked(user string) int {
	return int(s.ticket[user]-s.served) + 1
}

func (s *Store) setFree(n int) {
	s.free = n
	s.left.Store(int64(n))
}
