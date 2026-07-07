# Makro GUI (`cmd/gui`)

The desktop app: **Electron shell + Vue3 SPA + Go HTTP/WS server** (`makro-serve`).

- **Go server**: `go build -o bin/makro-serve ./cmd/gui/` — launched by Electron as `makro-serve serve`.
- **Frontend**: `cd frontend && npm run build` (Vite) — output served by the Go server at `/`.
- **iOS** talks to the same Go server over HTTPS/WS.

HTTP/WS API surface, layering, and runtime workflows live in [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md).
