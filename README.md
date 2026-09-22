# Flight Concierge

A Go flight assistant that combines Gemini natural-language extraction with live Duffel flight searches. It supports a terminal conversation and a browser UI with demo login, chatbot search, profile-based recommendations, flight ratings, and pending bookings.

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
$env:GEMINI_MODEL = "gemini-3.5-flash-lite"
```

`.env` is ignored by Git and is not loaded automatically by the application. Do not commit API keys. Rotate any key that has been exposed.

## Run The Browser UI

```powershell
$env:WEB_MODE = "1"
go run ./cmd/flight-agent
```

Open <http://localhost:8080>. The UI uses the booking assistant as its primary search input. Enter a request such as:

```text
Find the cheapest one-way economy flight for 1 person from Delhi to Port Blair with a layover in Kolkata on December 19, 2026.
```

Gemini extracts the structured search details, then Duffel searches live offers. The UI can:

- Require a connection through a named airport when the preference requests a layover.
- Accept any layover duration when no duration is specified.
- Rank and highlight flights using the user's booking profile.
- Show relative price, duration/delay-risk, and reliability ratings. Historical reliability is marked unavailable when Duffel does not provide it.
- Sort returned flights by recommended order, lowest price, or shortest duration.
- Open full flight details and start a booking conversation.

The booking flow stores a `pending_confirmation` booking in memory. It does not yet process payment or create a live Duffel order.

Demo accounts use password `demo`:

- `alice@example.com`
- `ben@example.com`
- `carla@example.com`
- `dev@example.com`

Each demo user has at least 10 seeded bookings. The active profile is selected by the logged-in demo account.

To use a different port:

```powershell
$env:WEB_ADDR = ":8081"
```

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

The booking store is intentionally in memory and resets when the process restarts. The demo login is not suitable for production: replace it with hashed passwords, persistent storage, CSRF protection, session expiry, secure cookies, and real Duffel order/payment handling before deployment.
