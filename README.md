# Flight Concierge

A Go flight assistant that combines Gemini tool calling with live Duffel flight searches. It supports both a terminal conversation and a small browser UI with demo login, profile preferences, and recent bookings.

## Layout

```text
cmd/flight-agent/  Go executable, web server, booking store, and tests
examples/          Sample flight prompts
.env.example       Environment variable template
```

## Configuration

Copy `.env.example` to `.env`, then provide `GEMINI_API_KEY` and `DUFFEL_API_TOKEN`. The application does not load `.env` automatically; set the variables in PowerShell or use your preferred environment loader.

```powershell
$env:GEMINI_API_KEY = "your-gemini-key"
$env:DUFFEL_API_TOKEN = "your-duffel-token"
$env:DUFFEL_TO_INR_RATE = "86.5"
```

## Run The Browser UI

```powershell
$env:WEB_MODE = "1"
go run ./cmd/flight-agent
```

Open <http://localhost:8080>. Demo accounts use password `demo`:

- `alice@example.com`
- `ben@example.com`
- `carla@example.com`
- `dev@example.com`

## Run The Terminal App

```powershell
Remove-Item Env:WEB_MODE -ErrorAction SilentlyContinue
go run ./cmd/flight-agent
```

## Test

```powershell
go test ./...
go vet ./...
```

The booking store is intentionally in memory and resets when the process restarts. The demo login is not suitable for production: replace it with hashed passwords, persistent storage, CSRF protection, session expiry, and secure cookies before deployment.
