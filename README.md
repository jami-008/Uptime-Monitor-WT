# PulseWatch — Uptime Monitoring System (Go)

A Web Technology course project built with **Go (Golang)**, standard-library HTTP server, HTML/CSS/JavaScript dashboard, and optional **Telegram bot alerts**. No Go dependencies or external database needed.

## Features

- Add a website or HTTP service using its URL
- Automatically send HTTP GET requests every 5–3600 seconds
- Display **UP**, **DOWN**, or **PENDING** status
- Mark **DOWN** after 2 consecutive failed checks (reduces false alerts)
- Send a **DOWN alert** and a **RECOVERED alert** on status changes
- Show HTTP code, latency, uptime percentage, and recent check history
- Manually trigger a check and delete monitors
- Store sites and history in `data/uptime.json`, even after restarting
- Dashboard refreshes automatically every 5 seconds

## Requirements

- Go **1.22+** ([install Go](https://go.dev/dl/))
- A web browser

## Quick start

Open a terminal in the project folder:

```bash
go run .
```

Visit **http://localhost:8080** in your browser.

Try adding:

- Name: `Google`
- URL: `https://www.google.com`
- Check interval: `10 seconds`

A check starts automatically, usually within one second. To simulate a failure, monitor a local HTTP service and stop it, or use a test URL that returns HTTP 500. After 2 consecutive failed checks the site status changes to DOWN.

## Optional: Telegram alerts

1. Open Telegram and message **@BotFather**.
2. Send `/newbot`, follow the instructions, and copy the **bot token**.
3. Send a message to your new bot.
4. Open `https://api.telegram.org/bot<YOUR_TOKEN>/getUpdates` in your browser; find `message.chat.id` in the JSON response. (If you use other webhook/update setups, retrieve the chat ID using the appropriate method.)
5. Set these environment variables **before** running the program:

**Linux / macOS:**

```bash
export TELEGRAM_BOT_TOKEN="your-bot-token"
export TELEGRAM_CHAT_ID="your-chat-id"
go run .
```

**Windows PowerShell:**

```powershell
$env:TELEGRAM_BOT_TOKEN="your-bot-token"
$env:TELEGRAM_CHAT_ID="your-chat-id"
go run .
```

If these variables are absent, alerts still appear in the Go server's terminal (and state changes are shown in dashboard history), but no Telegram message is sent.

> Keep the bot token secret: never paste it into frontend JavaScript or commit it to Git.

## Folder structure

```text
go-uptime-monitor/
├── go.mod                # Go module
├── main.go               # REST API, scheduler, checker, JSON storage, alerts
├── main_test.go          # Go tests
├── web/
│   ├── index.html        # Dashboard markup
│   ├── style.css         # Responsive layout
│   └── app.js            # Fetch API + UI interactions
├── data/
│   └── .gitkeep          # uptime.json will be created here automatically
├── .gitignore
└── README.md
```

## API endpoints

| Method | Endpoint | Description |
|---|---|---|
| GET | `/api/sites` | List monitors |
| POST | `/api/sites` | Add monitor |
| DELETE | `/api/sites/{id}` | Remove monitor |
| POST | `/api/sites/{id}/check` | Check monitor immediately |
| GET | `/api/history` | Recent 80 check results |
| GET | `/api/health` | Server health |

Example add-monitor request:

```bash
curl -X POST http://localhost:8080/api/sites \
  -H "Content-Type: application/json" \
  -d '{"name":"My Site","url":"https://example.com","interval_seconds":30}'
```

## How it works

```text
User enters a website URL
           ↓
Go REST API validates and stores it
           ↓
Scheduler runs every 1 second
           ↓
When monitor is due, Go sends an HTTP GET request
           ↓
HTTP 200–399?    → UP / RECOVERED (if previously DOWN)
HTTP 400+, error → Increase consecutive failure count
           ↓
Two failures? → DOWN → Telegram alert / terminal log
           ↓
Result saved to JSON + dashboard history updated
           ↓
Repeat after configured interval
```

Notes: TCP/DNS/TLS/timeouts also count as failures. HTTP redirects are followed automatically. The check requests have an 8-second timeout. To calculate uptime, PulseWatch uses `successful_checks / total_checks × 100`; this is *sampled check availability*, not exact wall-clock uptime or SLA-grade monitoring.

## Running tests

```bash
go test ./...
```

## Useful viva/presentation points

- **Backend:** Go `net/http`, JSON REST API, goroutines, mutex, `time.Ticker`
- **Frontend:** HTML, CSS, JavaScript `fetch`, DOM updates
- **HTTP:** GET status code, 2xx/3xx success, error/timeout failure
- **Concurrency:** Each website can be checked in its own goroutine; mutex protects shared site state
- **Reliability:** Two failures before marking down; atomic JSON file writes
- **Notifications:** Telegram Bot API on state transitions

## Limitations / production security

- **Local development only by default.** Server binds to `127.0.0.1`, and the API does not require authentication. Do **not** expose it on a public network without implementing authentication, rate limiting, URL/IP allowlisting / SSRF protection, and HTTPS.
- `BIND_ALL=1` can make it listen on all interfaces, but **this is not safe for untrusted public access** as currently written.
- The checker can access internal IPs and localhost; this supports local-service demos but is unsafe when exposed to strangers.
- This is a course-project MVP, not an enterprise monitoring or SLA system. Real systems use persistent SQL databases, authenticated users, background worker queues, and distributed checks.
- The history contains the latest 300 completed checks in JSON. The API shows the latest 80.

## Configuration

| Environment variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Web server port |
| `DATA_FILE` | `data/uptime.json` | Storage file location |
| `TELEGRAM_BOT_TOKEN` | unset | Telegram bot token |
| `TELEGRAM_CHAT_ID` | unset | Telegram chat ID |
| `BIND_ALL` | unset | If `1`, bind to all network interfaces (see security warning) |
# Uptime-Monitor-WT
