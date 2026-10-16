// Package sim runs a whole drop in a sandbox and reports what happened, for
// the simulator on the page. Each run gets its own store, fake payment
// company and HTTP server on a loopback port, so it never touches the live
// drop, and every click goes through real HTTP requests.
package sim

import (
	"bytes"
	"cmp"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"sneakdrop/drop"
	"sneakdrop/payments"
	"sneakdrop/server"
)

const (
	stock    = 20
	payers   = 15 // winners who pay; the rest let their hold run out
	lineSize = 7  // buyers who missed out and join the waiting line

	MaxBuyers   = 5000
	MaxWindowMs = 5000
)

type Options struct {
	Buyers   int `json:"buyers"`    // different users, each clicks Buy once
	WindowMs int `json:"window_ms"` // clicks are spread evenly over this many ms; 0 means all at once
}

type Report struct {
	Passed    bool           `json:"passed"`
	Buyers    int            `json:"buyers"`
	Clicks    int            `json:"clicks"`
	Stock     int            `json:"stock"`
	WindowMs  int            `json:"window_ms"`
	HoldTTLMs int64          `json:"hold_ttl_ms"`
	RushMs    int64          `json:"rush_ms"`
	Outcomes  map[string]int `json:"outcomes"`
	Winners   []Winner       `json:"winners"`
	Line      []Waiter       `json:"line"`
	Messages  []Message      `json:"messages"`
	Checks    []Check        `json:"checks"`
	Final     drop.Stats     `json:"final"`
}

type Winner struct {
	Order  int    `json:"order"` // the order the server granted holds in
	User   string `json:"user"`
	Click  int    `json:"click"` // 1 is the first click sent
	HoldID int64  `json:"hold_id"`
	Fate   string `json:"fate"`
}

type Waiter struct {
	Position int    `json:"position"`
	User     string `json:"user"`
	Fate     string `json:"fate"`
}

// Message is one payment webhook the sandbox shop received.
type Message struct {
	AtMs    int64  `json:"at_ms"`
	Kind    string `json:"kind"`
	EventID string `json:"event_id"`
	HoldID  int64  `json:"hold_id"`
	Status  int    `json:"status"`
	Outcome string `json:"outcome"`
	Source  string `json:"source"` // "provider" or "simulator"
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Info   bool   `json:"info,omitempty"` // an observation, not a rule
	Detail string `json:"detail,omitempty"`
}

var running sync.Mutex

// Handler serves POST /api/simulate. One simulation runs at a time.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var opt Options
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&opt); err != nil ||
			opt.Buyers < 1 || opt.Buyers > MaxBuyers || opt.WindowMs < 0 || opt.WindowMs > MaxWindowMs {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("buyers must be 1 to %d and window_ms 0 to %d", MaxBuyers, MaxWindowMs),
			})
			return
		}
		if !running.TryLock() {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "a simulation is already running"})
			return
		}
		defer running.Unlock()

		rep, err := Run(r.Context(), opt)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rep)
	}
}

type click struct {
	n      int // 1 is the first click sent
	user   string
	result string
	holdID int64
}

// Run plays one drop from start to finish:
//
//  1. the rush: every buyer clicks Buy, every 20th buyer double clicks
//  2. the first 15 winners pay, through a provider that repeats every message
//  3. the earliest buyers who missed out join the waiting line
//  4. the 5 unpaid holds run out and pass to the line
//  5. the simulator sends a late payment and a forged one
func Run(ctx context.Context, opt Options) (*Report, error) {
	window := time.Duration(opt.WindowMs) * time.Millisecond
	// Long enough that winners can pay after the rush, short enough to watch.
	ttl := window + 3*time.Second
	rep := &Report{Buyers: opt.Buyers, Stock: stock, WindowMs: opt.WindowMs, HoldTTLMs: ttl.Milliseconds(), Outcomes: map[string]int{}}
	c := &checker{rep: rep}

	var passedMu sync.Mutex
	passedTo := map[int64]drop.Hold{} // expired hold id -> hold the pair went to
	store := drop.New(drop.Config{Stock: stock, HoldTTL: ttl, OnExpire: func(h drop.Hold, next *drop.Hold) {
		if next != nil {
			passedMu.Lock()
			passedTo[h.ID] = *next
			passedMu.Unlock()
		}
	}})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	base := "http://" + ln.Addr().String()
	secret := []byte(crand.Text())
	pay := payments.New(base+"/webhooks/payment", secret)
	pay.MaxDelay = 300 * time.Millisecond
	pay.DuplicateRate = 1
	pay.FailureRate = 1

	rec := &recorder{start: time.Now()}
	srv := &http.Server{Handler: rec.wrap(server.New(store, pay, secret, false))}
	go srv.Serve(ln)
	defer srv.Close()

	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	go store.Run(sweepCtx, 50*time.Millisecond)

	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 1000}}
	defer client.CloseIdleConnections()

	// 1. The rush.
	clicks := plan(opt.Buyers)
	rep.Clicks = len(clicks)
	rushStart := time.Now().Add(20 * time.Millisecond) // give every goroutine time to get ready
	var wg sync.WaitGroup
	for i := range clicks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Until(rushStart.Add(window * time.Duration(i) / time.Duration(len(clicks)))))
			clicks[i].result, clicks[i].holdID = buy(client, base, clicks[i].user)
		}()
	}
	wg.Wait()
	rep.RushMs = time.Since(rushStart).Milliseconds()

	var winners []click
	heldBy := map[string]int{}
	clickedBy := map[string]int{}
	for _, cl := range clicks {
		rep.Outcomes[cl.result]++
		clickedBy[cl.user]++
		if cl.result == "held" {
			winners = append(winners, cl)
			heldBy[cl.user]++
		}
	}
	slices.SortFunc(winners, func(a, b click) int { return cmp.Compare(a.holdID, b.holdID) })

	held := len(winners)
	turnedAway := rep.Outcomes["sold_out"] + rep.Outcomes["already_holding"]
	doubleClickers, doubleHeld := 0, 0
	for user, n := range clickedBy {
		if n > 1 {
			doubleClickers++
		}
		if heldBy[user] > 1 {
			doubleHeld++
		}
	}
	early := 0
	for _, w := range winners {
		if w.n <= stock {
			early++
		}
	}
	c.expect(held == min(stock, opt.Buyers), fmt.Sprintf("exactly %d pairs held, never more", min(stock, opt.Buyers)),
		fmt.Sprintf("%d of %d clicks got a hold", held, len(clicks)))
	c.expect(len(heldBy) == held, "every held pair belongs to a different buyer", "")
	c.expect(doubleHeld == 0, "nobody holds two pairs", fmt.Sprintf("%d buyers double clicked; none got two", doubleClickers))
	c.expect(turnedAway == len(clicks)-held, "every other click was turned away cleanly",
		fmt.Sprintf("%d sold_out, %d already_holding, %d errors", rep.Outcomes["sold_out"], rep.Outcomes["already_holding"], len(clicks)-held-turnedAway))
	order := "all clicks left at the same instant, so the network decided which reached the server first"
	if window > 0 {
		order = fmt.Sprintf("clicks %.2fms apart can still overtake each other on the way in", float64(window)/float64(time.Millisecond)/float64(len(clicks)))
	}
	c.note(fmt.Sprintf("%d of the %d winners were among the first %d clicks", early, held, stock),
		order+"; the server's lock decides who is first, and the hold ids record that order")
	c.books(store.Stats(), "the books balance after the rush")

	for i, w := range winners {
		rep.Winners = append(rep.Winners, Winner{Order: i + 1, User: w.user, Click: w.n, HoldID: w.holdID})
	}
	if held != stock {
		// Too few buyers to sell out; the later steps need a sold-out drop.
		return finish(rep, store, rec), nil
	}

	// 2. The first winners pay. Every success arrives twice, plus a stray failure.
	paidEvent := map[string]string{}
	var payMu sync.Mutex
	for _, w := range winners[:payers] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out struct {
				EventID string `json:"event_id"`
			}
			postJSON(client, base+"/api/pay", map[string]any{"user": w.user, "hold_id": w.holdID}, &out)
			payMu.Lock()
			paidEvent[w.user] = out.EventID
			payMu.Unlock()
		}()
	}
	wg.Wait()
	confirmed := waitFor(ctx, 5*time.Second, func() bool { return store.Stats().Sold == payers })
	sleep(ctx, pay.MaxDelay+200*time.Millisecond) // let the repeats land too
	st := store.Stats()
	c.expect(confirmed && st.Sold == payers && st.RefundsDue == 0, fmt.Sprintf("%d payments confirmed, each exactly once", payers),
		fmt.Sprintf("%d sold; %d repeated messages and %d stray failures changed nothing", st.Sold, rec.count("duplicate"), rec.count("failure_noted")))

	// 3. The earliest buyers who missed out join the line.
	var line []string
	inLine := map[string]bool{}
	for _, cl := range clicks {
		if len(line) == lineSize {
			break
		}
		if cl.result == "sold_out" && heldBy[cl.user] == 0 && !inLine[cl.user] {
			line = append(line, cl.user)
			inLine[cl.user] = true
		}
	}
	placesOK := true
	for i, user := range line {
		var out struct {
			Position int `json:"position"`
		}
		postJSON(client, base+"/api/queue", map[string]string{"user": user}, &out)
		rep.Line = append(rep.Line, Waiter{Position: out.Position, User: user})
		placesOK = placesOK && out.Position == i+1
	}
	c.expect(placesOK, fmt.Sprintf("%d buyers who missed out joined the line at places 1 to %d", len(line), len(line)), "")

	// 4. The unpaid holds run out and pass to the line.
	unpaid := winners[payers:]
	handoffs := min(len(unpaid), len(line))
	expired := waitFor(ctx, ttl+2*time.Second, func() bool {
		st := store.Stats()
		return st.Held == handoffs && st.Waiting == len(line)-handoffs
	})
	passedMu.Lock()
	passed := maps.Clone(passedTo)
	passedMu.Unlock()

	for i := range rep.Winners {
		w := &rep.Winners[i]
		switch next, ok := passed[w.HoldID]; {
		case store.View(w.User).Purchases > 0:
			w.Fate = "paid"
		case ok:
			w.Fate = "did not pay; hold ran out, pair passed to " + next.User
		default:
			w.Fate = "did not pay; hold ran out, pair back on sale"
		}
	}
	inOrder, fresh := true, true
	for i := range rep.Line {
		wt := &rep.Line[i]
		v := store.View(wt.User)
		switch {
		case v.Hold != nil:
			wt.Fate = fmt.Sprintf("was given a pair when a hold ran out (hold #%d, %ds to pay)", v.Hold.ID, v.Hold.ExpiresInMs/1000)
			inOrder = inOrder && i < handoffs
			fresh = fresh && v.Hold.ExpiresInMs > (ttl-time.Second).Milliseconds()
		case v.QueuePosition != nil:
			wt.Fate = fmt.Sprintf("still waiting, moved up to number %d", *v.QueuePosition)
			inOrder = inOrder && i >= handoffs && *v.QueuePosition == i-handoffs+1
		default:
			wt.Fate = "?"
			inOrder = false
		}
	}
	c.expect(expired && inOrder, fmt.Sprintf("the %d unpaid holds ran out and went to the first %d in line, in order", len(unpaid), handoffs),
		fmt.Sprintf("the other %d moved up", len(line)-handoffs))
	c.expect(fresh, "each passed-on pair came with a fresh hold of its own", fmt.Sprintf("%ds to pay", ttl/time.Second))

	// 5. The payment company misbehaves on purpose.
	first := winners[0]
	status, out := sendWebhook(client, base, secret, payments.Event{ID: paidEvent[first.user], HoldID: first.holdID, Kind: payments.KindSuccess})
	c.expect(status == http.StatusOK && out == string(drop.OutcomeDuplicate), "a replayed success changes nothing", out)

	late := unpaid[0]
	status, out = sendWebhook(client, base, secret, payments.Event{ID: "sim-late-" + crand.Text(), HoldID: late.holdID, Kind: payments.KindSuccess})
	c.expect(status == http.StatusOK && out == string(drop.OutcomeRefundRequired),
		"money that arrives after its hold ran out is flagged for refund", late.user+"'s pair is not taken back from its new owner")

	status, out = sendWebhook(client, base, []byte("wrong secret"), payments.Event{ID: "sim-forged-" + crand.Text(), HoldID: unpaid[1].holdID, Kind: payments.KindSuccess})
	c.expect(status == http.StatusUnauthorized, "a payment message with a forged signature is rejected", fmt.Sprintf("HTTP %d %s", status, out))

	return finish(rep, store, rec), nil
}

func finish(rep *Report, store *drop.Store, rec *recorder) *Report {
	rep.Final = store.Stats()
	(&checker{rep: rep}).books(rep.Final, "the final books balance")
	rep.Messages = rec.list()
	rep.Passed = true
	for _, ch := range rep.Checks {
		rep.Passed = rep.Passed && ch.OK
	}
	return rep
}

// plan lists the clicks in the order they are sent. Every 20th buyer clicks
// twice in a row, to show that nobody can hold two pairs.
func plan(buyers int) []click {
	var cs []click
	for b := 1; b <= buyers; b++ {
		user := fmt.Sprintf("buyer-%04d", b)
		cs = append(cs, click{n: len(cs) + 1, user: user})
		if b%20 == 0 {
			cs = append(cs, click{n: len(cs) + 1, user: user})
		}
	}
	return cs
}

func buy(client *http.Client, base, user string) (string, int64) {
	var out struct {
		Hold  *drop.Hold `json:"hold"`
		Error string     `json:"error"`
	}
	status, err := postJSON(client, base+"/api/hold", map[string]string{"user": user}, &out)
	switch {
	case err != nil:
		return "error", 0
	case status == http.StatusCreated && out.Hold != nil:
		return "held", out.Hold.ID
	case out.Error != "":
		return out.Error, 0
	default:
		return fmt.Sprintf("http_%d", status), 0
	}
}

func postJSON(client *http.Client, url string, in, out any) (int, error) {
	body, _ := json.Marshal(in)
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

func sendWebhook(client *http.Client, base string, secret []byte, ev payments.Event) (int, string) {
	body, _ := json.Marshal(ev)
	req, _ := http.NewRequest(http.MethodPost, base+"/webhooks/payment", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(payments.SignatureHeader, payments.Sign(secret, body))
	req.Header.Set(sentByHeader, "simulator")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	var out struct {
		Outcome string `json:"outcome"`
		Error   string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Outcome + out.Error
}

func waitFor(ctx context.Context, limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		sleep(ctx, 20*time.Millisecond)
	}
	return true
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

type checker struct{ rep *Report }

func (c *checker) expect(ok bool, name, detail string) {
	c.rep.Checks = append(c.rep.Checks, Check{Name: name, OK: ok, Detail: detail})
}

func (c *checker) note(name, detail string) {
	c.rep.Checks = append(c.rep.Checks, Check{Name: name, OK: true, Info: true, Detail: detail})
}

func (c *checker) books(st drop.Stats, name string) {
	c.expect(st.Available+st.Held+st.Sold == st.Total, name,
		fmt.Sprintf("available %d + held %d + sold %d = %d of %d", st.Available, st.Held, st.Sold, st.Available+st.Held+st.Sold, st.Total))
}

// sentByHeader marks the webhooks the simulator sends itself.
const sentByHeader = "X-Sent-By"

// recorder notes every payment webhook the sandbox shop receives and what
// the shop answered.
type recorder struct {
	start time.Time
	mu    sync.Mutex
	msgs  []Message
}

func (r *recorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/webhooks/payment" {
			next.ServeHTTP(w, req)
			return
		}
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		cw := &captureWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(cw, req)

		var ev payments.Event
		json.Unmarshal(body, &ev)
		var resp struct {
			Outcome string `json:"outcome"`
			Error   string `json:"error"`
		}
		json.Unmarshal(cw.body.Bytes(), &resp)
		source := "provider"
		if req.Header.Get(sentByHeader) == "simulator" {
			source = "simulator"
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.msgs = append(r.msgs, Message{
			AtMs: time.Since(r.start).Milliseconds(), Kind: ev.Kind, EventID: ev.ID, HoldID: ev.HoldID,
			Status: cw.status, Outcome: resp.Outcome + resp.Error, Source: source,
		})
	})
}

func (r *recorder) count(outcome string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.msgs {
		if m.Outcome == outcome {
			n++
		}
	}
	return n
}

func (r *recorder) list() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.msgs)
}

type captureWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
