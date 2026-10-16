// Command sneakdrop runs the drop: API, status page, hold sweeper and the
// fake payment provider, all in one process.
package main

import (
	"context"
	crand "crypto/rand"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"sneakdrop/drop"
	"sneakdrop/payments"
	"sneakdrop/server"
	"sneakdrop/sim"
)

const sweepEvery = 200 * time.Millisecond

func main() {
	port := envString("PORT", "8080")
	stock := envInt("STOCK", 20)
	ttl := envDuration("HOLD_TTL", 5*time.Minute)
	// Reset is on for the demo. Set ALLOW_RESET=0 for a real sale.
	allowReset := os.Getenv("ALLOW_RESET") != "0"

	// The provider and the shop share this secret. When it is not set, both
	// halves run in this process anyway, so a random one works.
	secret := []byte(os.Getenv("WEBHOOK_SECRET"))
	if len(secret) == 0 {
		secret = []byte(crand.Text())
	}

	store := drop.New(drop.Config{Stock: stock, HoldTTL: ttl, OnExpire: logExpiry})

	pay := payments.New(envString("WEBHOOK_URL", "http://127.0.0.1:"+port+"/webhooks/payment"), secret)
	pay.MaxDelay = envDuration("PAY_MAX_DELAY", pay.MaxDelay)
	pay.DuplicateRate = envFloat("PAY_DUPLICATE_RATE", pay.DuplicateRate)
	pay.FailureRate = envFloat("PAY_FAILURE_RATE", pay.FailureRate)
	pay.Logf = log.Printf

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go store.Run(ctx, sweepEvery)

	mux := http.NewServeMux()
	mux.Handle("/", server.New(store, pay, secret, allowReset))
	mux.Handle("POST /api/simulate", sim.Handler())

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Printf("sneakdrop on http://localhost:%s  stock=%d hold=%v reset=%v", port, stock, ttl, allowReset)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func logExpiry(h drop.Hold, next *drop.Hold) {
	if next != nil {
		log.Printf("hold %d for %s ran out; pair goes to %s (hold %d)", h.ID, h.User, next.User, next.ID)
	} else {
		log.Printf("hold %d for %s ran out; pair is back on sale", h.ID, h.User)
	}
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Fatalf("%s must be a whole number, got %q", key, v)
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Fatalf("%s must be a positive duration like 30s or 5m, got %q", key, v)
	}
	return d
}

func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		log.Fatalf("%s must be between 0 and 1, got %q", key, v)
	}
	return f
}
