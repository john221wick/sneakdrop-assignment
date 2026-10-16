// Package server is the HTTP layer: the JSON API, the payment webhook and the
// status page. The rules themselves live in package drop.
package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"sneakdrop/drop"
	"sneakdrop/payments"
)

//go:embed page.html
var page []byte

const (
	maxUserLen    = 100
	maxEventIDLen = 200
	maxBodyBytes  = 4 << 10
)

// Charger starts a payment for a hold. payments.Provider implements it.
type Charger interface {
	Charge(holdID int64) (eventID string)
}

type Server struct {
	store      *drop.Store
	payer      Charger
	secret     []byte
	allowReset bool
}

func New(store *drop.Store, payer Charger, webhookSecret []byte, allowReset bool) http.Handler {
	s := &Server{store: store, payer: payer, secret: webhookSecret, allowReset: allowReset}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("POST /api/hold", s.hold)
	mux.HandleFunc("POST /api/queue", s.joinLine)
	mux.HandleFunc("POST /api/pay", s.pay)
	mux.HandleFunc("POST /webhooks/payment", s.webhook)
	mux.HandleFunc("POST /admin/reset", s.reset)
	return mux
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

type stateResponse struct {
	drop.View
	Message *string `json:"message"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	user := strings.TrimSpace(r.URL.Query().Get("user"))
	if len(user) > maxUserLen {
		writeError(w, http.StatusBadRequest, "bad_user")
		return
	}
	v := s.store.View(user)
	writeJSON(w, http.StatusOK, stateResponse{View: v, Message: message(v)})
}

func message(v drop.View) *string {
	var m string
	switch {
	case v.Hold != nil:
		m = "Pair held. Pay before the timer runs out."
	case v.QueuePosition != nil:
		m = fmt.Sprintf("You are position %d in line.", *v.QueuePosition)
	case v.Available == 0:
		m = "Sold out. Join the waiting line."
	default:
		return nil
	}
	return &m
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Stats())
}

func (s *Server) hold(w http.ResponseWriter, r *http.Request) {
	user, ok := readUser(w, r)
	if !ok {
		return
	}
	h, err := s.store.Hold(user)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"hold": h})
}

func (s *Server) joinLine(w http.ResponseWriter, r *http.Request) {
	user, ok := readUser(w, r)
	if !ok {
		return
	}
	pos, err := s.store.JoinLine(user)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"position": pos})
}

func (s *Server) pay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		User   string `json:"user"`
		HoldID int64  `json:"hold_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.HoldID <= 0 {
		writeError(w, http.StatusBadRequest, "missing_hold")
		return
	}
	if err := s.store.CheckPayable(body.HoldID, strings.TrimSpace(body.User)); err != nil {
		writeStoreError(w, err)
		return
	}
	// The result arrives later, through the webhook.
	eventID := s.payer.Charge(body.HoldID)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending", "event_id": eventID})
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !payments.Verify(s.secret, body, r.Header.Get(payments.SignatureHeader)) {
		writeError(w, http.StatusUnauthorized, "bad_signature")
		return
	}
	var ev payments.Event
	if err := json.Unmarshal(body, &ev); err != nil ||
		ev.ID == "" || len(ev.ID) > maxEventIDLen || ev.HoldID <= 0 ||
		(ev.Kind != payments.KindSuccess && ev.Kind != payments.KindFailed) {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}

	out := s.store.ApplyPayment(ev.ID, ev.HoldID, drop.PaymentKind(ev.Kind))
	log.Printf("webhook %s %s hold %d -> %s", ev.Kind, ev.ID, ev.HoldID, out)
	// Always 200 once the event is understood, even for duplicates and late
	// payments. Anything else makes the provider retry forever.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "outcome": out})
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	if !s.allowReset {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	s.store.Reset()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func readUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		User string `json:"user"`
	}
	if !decode(w, r, &body) {
		return "", false
	}
	user := strings.TrimSpace(body.User)
	if user == "" || len(user) > maxUserLen {
		writeError(w, http.StatusBadRequest, "missing_user")
		return "", false
	}
	return user, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return false
	}
	return true
}

func writeStoreError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, drop.ErrHoldNotFound) {
		status = http.StatusNotFound
	}
	writeError(w, status, err.Error())
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
