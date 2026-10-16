package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sneakdrop/drop"
	"sneakdrop/payments"
)

var secret = []byte("test-secret")

type stubPayer struct{ charged []int64 }

func (p *stubPayer) Charge(holdID int64) string {
	p.charged = append(p.charged, holdID)
	return "evt_stub"
}

func newTestServer(t *testing.T, payer Charger) (*httptest.Server, *drop.Store) {
	t.Helper()
	store := drop.New(drop.Config{Stock: 20, HoldTTL: time.Minute})
	ts := httptest.NewServer(New(store, payer, secret, true))
	t.Cleanup(ts.Close)
	return ts, store
}

func call(t *testing.T, method, url, body string, header http.Header) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func signedWebhook(t *testing.T, url string, ev payments.Event) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(ev)
	h := http.Header{}
	h.Set(payments.SignatureHeader, payments.Sign(secret, body))
	return call(t, "POST", url+"/webhooks/payment", string(body), h)
}

func holdFor(t *testing.T, url, user string) int64 {
	t.Helper()
	status, out := call(t, "POST", url+"/api/hold", `{"user":"`+user+`"}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("hold: %d %v", status, out)
	}
	return int64(out["hold"].(map[string]any)["id"].(float64))
}

func TestHoldThenState(t *testing.T) {
	ts, _ := newTestServer(t, &stubPayer{})
	id := holdFor(t, ts.URL, "ana")

	status, out := call(t, "POST", ts.URL+"/api/hold", `{"user":"ana"}`, nil)
	if status != http.StatusConflict || out["error"] != "already_holding" {
		t.Fatalf("second hold: %d %v", status, out)
	}

	_, state := call(t, "GET", ts.URL+"/api/state?user=ana", "", nil)
	hold, _ := state["hold"].(map[string]any)
	if state["available"] != 19.0 || hold == nil || hold["id"] != float64(id) {
		t.Fatalf("state %v", state)
	}
}

func TestBadRequests(t *testing.T) {
	ts, _ := newTestServer(t, &stubPayer{})
	for _, tc := range []struct{ path, body string }{
		{"/api/hold", `{}`},
		{"/api/hold", `not json`},
		{"/api/queue", `{"user":"   "}`},
		{"/api/pay", `{"user":"ana"}`},
	} {
		if status, _ := call(t, "POST", ts.URL+tc.path, tc.body, nil); status != http.StatusBadRequest {
			t.Errorf("%s %s: %d, want 400", tc.path, tc.body, status)
		}
	}
}

func TestPayOnlyForAnActiveHold(t *testing.T) {
	payer := &stubPayer{}
	ts, _ := newTestServer(t, payer)

	if status, _ := call(t, "POST", ts.URL+"/api/pay", `{"user":"ana","hold_id":999}`, nil); status != http.StatusNotFound {
		t.Fatalf("unknown hold: %d", status)
	}
	id := holdFor(t, ts.URL, "ana")
	if status, _ := call(t, "POST", ts.URL+"/api/pay", `{"user":"ben","hold_id":`+itoa(id)+`}`, nil); status != http.StatusNotFound {
		t.Fatalf("someone else's hold: %d", status)
	}
	if status, _ := call(t, "POST", ts.URL+"/api/pay", `{"user":"ana","hold_id":`+itoa(id)+`}`, nil); status != http.StatusAccepted {
		t.Fatalf("own hold: %d", status)
	}
	if len(payer.charged) != 1 || payer.charged[0] != id {
		t.Fatalf("charged %v", payer.charged)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	ts, _ := newTestServer(t, &stubPayer{})
	id := holdFor(t, ts.URL, "ana")
	body := `{"event_id":"evt1","hold_id":` + itoa(id) + `,"kind":"success"}`
	h := http.Header{}
	h.Set(payments.SignatureHeader, "forged")
	if status, _ := call(t, "POST", ts.URL+"/webhooks/payment", body, h); status != http.StatusUnauthorized {
		t.Fatalf("forged webhook: %d", status)
	}
	if _, state := call(t, "GET", ts.URL+"/api/state?user=ana", "", nil); state["purchases"] != 0.0 {
		t.Fatal("a forged webhook paid for a hold")
	}
}

func TestWebhookDuplicateAndOutOfOrder(t *testing.T) {
	ts, _ := newTestServer(t, &stubPayer{})
	id := holdFor(t, ts.URL, "ana")

	steps := []struct {
		ev   payments.Event
		want drop.Outcome
	}{
		{payments.Event{ID: "evt-ok", HoldID: id, Kind: payments.KindSuccess}, drop.OutcomeApplied},
		{payments.Event{ID: "evt-ok", HoldID: id, Kind: payments.KindSuccess}, drop.OutcomeDuplicate},
		{payments.Event{ID: "evt-fail", HoldID: id, Kind: payments.KindFailed}, drop.OutcomeFailureNoted},
	}
	for _, step := range steps {
		status, out := signedWebhook(t, ts.URL, step.ev)
		if status != http.StatusOK || out["outcome"] != string(step.want) {
			t.Fatalf("%s %s: %d %v, want %s", step.ev.Kind, step.ev.ID, status, out, step.want)
		}
	}
	if _, state := call(t, "GET", ts.URL+"/api/state?user=ana", "", nil); state["purchases"] != 1.0 || state["sold"] != 1.0 {
		t.Fatalf("state %v", state)
	}
}

// TestPayEndToEnd runs the real fake provider against the real server, with
// every duplicate and failure turned on.
func TestPayEndToEnd(t *testing.T) {
	pay := payments.New("", secret)
	pay.MaxDelay = 50 * time.Millisecond
	pay.DuplicateRate = 1
	pay.FailureRate = 1
	ts, store := newTestServer(t, pay)
	pay.WebhookURL = ts.URL + "/webhooks/payment"

	id := holdFor(t, ts.URL, "ana")
	for range 2 { // a double click on Pay
		if status, _ := call(t, "POST", ts.URL+"/api/pay", `{"user":"ana","hold_id":`+itoa(id)+`}`, nil); status != http.StatusAccepted {
			t.Fatalf("pay: %d", status)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for store.View("ana").Purchases != 1 {
		if time.Now().After(deadline) {
			t.Fatal("payment never confirmed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let the duplicate and failure land too

	v := store.View("ana")
	if v.Purchases != 1 || v.Sold != 1 || v.RefundsDue != 0 || v.Hold != nil {
		t.Fatalf("view %+v", v)
	}
}

func TestResetIsGuarded(t *testing.T) {
	store := drop.New(drop.Config{Stock: 20, HoldTTL: time.Minute})
	ts := httptest.NewServer(New(store, &stubPayer{}, secret, false))
	defer ts.Close()
	if status, _ := call(t, "POST", ts.URL+"/admin/reset", "", nil); status != http.StatusForbidden {
		t.Fatalf("reset without ALLOW_RESET: %d", status)
	}
}

func TestPageServed(t *testing.T) {
	ts, _ := newTestServer(t, &stubPayer{})
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(buf.String(), "Sneaker Drop") {
		t.Fatalf("page: %d", resp.StatusCode)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
