# Sneakdrop Notes

## Requirements

- Go 1.27.2 or newer. The backend uses only the Go standard library.
- Node.js 22.12.0 or newer and pnpm 10.19.0 for the frontend.
- The optional HTTP verification script needs bash, curl, jq, openssl, and perl.

## Run the project

From the repository root, start the Go server:

```sh
cd backend
go run .
```

In a second terminal, start the storefront:

```sh
cd frontend
pnpm install
pnpm dev
```

Open the address printed by Vite, usually http://localhost:5173. The Go server also serves a plain status page at http://localhost:8080. The frontend uses `BACKEND_URL` if set; otherwise it connects to http://127.0.0.1:8080.

## How it works

The Go server manages 20 pairs in one in-memory store. A mutex protects inventory, active holds, purchase counts, the waiting line, and processed payment events. An atomic stock counter lets sold-out Buy requests return quickly. Each hold lasts five minutes, each shopper can hold one pair at a time, and each shopper can buy at most two pairs. Expired holds pass to the next eligible person in line.

The payment provider is simulated. It sends signed webhooks that may arrive more than once or out of order. The server ignores repeated event IDs and flags late successful payments for refund. The SvelteKit storefront shows stock, a hold countdown, checkout state, and queue position. Its simulator runs an isolated drop and reports the outcomes.

## Checks

```sh
cd backend
go test -race ./...
scripts/verify.sh
```

```sh
cd frontend
pnpm build
```

The store is in memory, so restarting the backend resets the drop. Demo reset is enabled by default; set `ALLOW_RESET=0` to disable that endpoint.
