// Package payments is a fake payment company. It takes a charge, answers
// right away, and later tells the shop how it went by POSTing a signed
// webhook, the way real providers do. Like real ones it is unreliable:
// messages come late, sometimes twice, and sometimes in the wrong order.
//
// It runs inside the same binary for convenience, but it only talks to the
// shop over HTTP, as an outside company would.
package payments

import (
	"bytes"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

const (
	KindSuccess = "success"
	KindFailed  = "failed"

	// SignatureHeader carries the hex HMAC-SHA256 of the request body.
	SignatureHeader = "X-Signature"
)

// Event is the webhook body.
type Event struct {
	ID     string `json:"event_id"`
	HoldID int64  `json:"hold_id"`
	Kind   string `json:"kind"`
}

func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func Verify(secret, body []byte, signature string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(signature))
}

type Provider struct {
	WebhookURL    string
	Secret        []byte
	MaxDelay      time.Duration // each message waits a random time up to this
	DuplicateRate float64       // chance the success message is sent twice
	FailureRate   float64       // chance an earlier failed attempt is reported too
	Client        *http.Client
	Logf          func(format string, args ...any)

	mu      sync.Mutex
	charged map[int64]string // hold id -> success event id
}

func New(webhookURL string, secret []byte) *Provider {
	return &Provider{
		WebhookURL:    webhookURL,
		Secret:        secret,
		MaxDelay:      3 * time.Second,
		DuplicateRate: 0.2,
		FailureRate:   0.15,
		Client:        &http.Client{Timeout: 5 * time.Second},
		Logf:          func(string, ...any) {},
	}
}

// Charge takes payment for a hold and returns the id of the success event it
// will send. The hold id works as an idempotency key: charging the same hold
// again (a double click on Pay) returns the first event id and takes no money.
func (p *Provider) Charge(holdID int64) string {
	p.mu.Lock()
	if id, ok := p.charged[holdID]; ok {
		p.mu.Unlock()
		return id
	}
	id := newEventID()
	if p.charged == nil {
		p.charged = map[int64]string{}
	}
	p.charged[holdID] = id
	p.mu.Unlock()

	success := Event{ID: id, HoldID: holdID, Kind: KindSuccess}
	p.sendLater(success)
	if rand.Float64() < p.DuplicateRate {
		// Same event id again: the shop must notice and ignore it.
		p.sendLater(success)
	}
	if rand.Float64() < p.FailureRate {
		// A card attempt that failed before this one went through. It gets its
		// own random delay, so it may land before or after the success.
		p.sendLater(Event{ID: newEventID(), HoldID: holdID, Kind: KindFailed})
	}
	return id
}

func (p *Provider) sendLater(ev Event) {
	var delay time.Duration
	if p.MaxDelay > 0 {
		delay = rand.N(p.MaxDelay)
	}
	p.Logf("payments: will send %s %s for hold %d in %v", ev.Kind, ev.ID, ev.HoldID, delay.Round(time.Millisecond))
	time.AfterFunc(delay, func() { p.deliver(ev) })
}

// deliver POSTs ev until the shop answers 2xx. Retries are another way the
// same event can reach the shop twice.
func (p *Provider) deliver(ev Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		p.Logf("payments: encode %s: %v", ev.ID, err)
		return
	}
	backoff := 250 * time.Millisecond
	for attempt := 1; attempt <= 5; attempt++ {
		err := p.post(body)
		if err == nil {
			return
		}
		p.Logf("payments: send %s attempt %d: %v", ev.ID, attempt, err)
		time.Sleep(backoff)
		backoff *= 2
	}
	p.Logf("payments: gave up on %s", ev.ID)
}

func (p *Provider) post(body []byte) error {
	req, err := http.NewRequest(http.MethodPost, p.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, Sign(p.Secret, body))
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("shop answered %s", resp.Status)
	}
	return nil
}

func newEventID() string {
	return "evt_" + crand.Text()
}
