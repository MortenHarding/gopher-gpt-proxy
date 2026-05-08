# GopherGPT

A Gopher proxy server written in Go that lets any Gopher client have a
multi-turn conversation with an open-weight LLM via **Groq**.

## Requirements

- Go 1.18+
- A free Groq API key — sign up at https://console.groq.com (no credit card required)

## Build

```bash
git clone <repo>
cd gophergpt
go build -o gophergpt .
```

Or run directly without building:

```bash
go run main.go
```

## Usage

```
GROQ_API_KEY=<your-key> ./gophergpt [options]

Options:
  -host        string   Hostname clients use to reach this server (default "localhost")
  -port        string   TCP port to listen on (default "7070")
  -access-log  string   Path to access log file, empty string disables logging (default "access.log")
```

### Examples

Run locally for testing:

```bash
export GROQ_API_KEY="gsk_..."
./gophergpt
```

Run on a public server:

```bash
export GROQ_API_KEY="gsk_..."
./gophergpt -host gopher.example.com -port 7070
```

Run with a custom log location:

```bash
export GROQ_API_KEY="gsk_..."
./gophergpt -host gopher.example.com -access-log /var/log/gophergpt/access.log
```

Disable access logging entirely:

```bash
export GROQ_API_KEY="gsk_..."
./gophergpt -access-log ""
```

Run on the standard Gopher port 70 (requires elevated privileges — see below):

```bash
export GROQ_API_KEY="gsk_..."
./gophergpt -host gopher.example.com -port 70
```

Then point your Gopher client at `gopher://localhost:7070/` (or your chosen host/port).

## How it works

```
Gopher client
    |
    |  TCP :7070
    v
+-----------------------------------------------------+
|  GopherGPT server                                   |
|                                                     |
|  net.Listen loop -> goroutine per connection        |
|                                                     |
|  SessionStore (sync.RWMutex + map)                  |
|    key  = client IP  (or explicit path token)       |
|    value = []Message (conversation history)         |
|    TTL  = 30 minutes (background reaper goroutine)  |
|                                                     |
|  Routes:                                            |
|    /          -> welcome menu                       |
|    /chat      -> Type-7 search -> send message      |
|    /new       -> reset session                      |
|    /history   -> show full conversation             |
+------------------------+----------------------------+
                         |  HTTPS POST /openai/v1/chat/completions
                         v
                   api.groq.com
```

## Selectors

| Selector   | Type | Description                       |
|------------|------|-----------------------------------|
| `/`        | menu | Welcome screen + navigation       |
| `/chat`    | 7    | Send a message, get a reply       |
| `/new`     | 7    | Reset conversation history        |
| `/history` | 1    | Read the full conversation so far |

## Access logging

Every request is appended to the access log as a tab-separated line:

```
<timestamp>    <client-ip>    <type>      <selector>    <elapsed>    <status>
2026-05-08T12:34:56Z    93.184.216.34    chat       /chat    243ms    ok
2026-05-08T12:35:01Z    93.184.216.34    chat       /chat    891ms    error: rate limit exceeded
```

Fields:

| Field      | Description                                              |
|------------|----------------------------------------------------------|
| timestamp  | UTC time in RFC 3339 format                              |
| client-ip  | Remote IP address of the connecting Gopher client        |
| type       | Request type: `menu`, `chat`, `new`, `history`, `error` |
| selector   | Raw Gopher selector string sent by the client            |
| elapsed    | Time from request receipt to response sent               |
| status     | `ok` or `error: <reason>`                                |

The log file is created automatically if it does not exist. Use `-access-log ""`
to disable logging entirely.

## Session identity

By default the server uses the **client's IP address** as the session key.

If multiple users share a NAT (e.g. a university gateway), embed an
explicit token in the path:

```
/chat/mysecrettoken
/new/mysecrettoken
/history/mysecrettoken
```

## Changing the model

Edit the `groqModel` constant in `main.go`:

```go
const groqModel = "llama-3.3-70b-versatile"
```

Browse all available models at: https://console.groq.com/docs/models

## Rate limits

Groq's free tier allows 14,400 requests/day and 6,000 tokens/minute on most
models — far more than enough for personal use. Full limits are listed at:
https://console.groq.com/docs/rate-limits

## Running on port 70 (production)

Port 70 requires elevated privileges. The cleanest approach on Linux:

```bash
sudo setcap cap_net_bind_service=+ep ./gophergpt
export GROQ_API_KEY="gsk_..."
./gophergpt -host gopher.example.com -port 70
```

Or run behind a TCP proxy (e.g. `socat`, `nginx stream`) that forwards
port 70 -> 7070.

## Docker build and run

Build the image, with your own Groq API key:

```bash
docker build -t mhardingdk/gopher:gptproxy .
```

Run the docker container:

```bash
docker run -e GROQ_API_KEY="gsk_..." -p 7070:7070 gopher-gpt-proxy .
```

To persist the access log outside the container, mount a volume:

```bash
docker run -e GROQ_API_KEY="gsk_..." \
  -p 7070:7070 \
  -v /var/log/gophergpt:/var/log/gophergpt \
  gopher-gpt-proxy \
  -access-log /var/log/gophergpt/access.log
```

## Notes

- The server uses **no third-party dependencies** — pure Go stdlib.
- Each client connection runs in its own goroutine; API calls are
  non-blocking relative to other connected clients.
- Responses are word-wrapped at 70 characters to respect the classic
  Gopher line-width convention.
- Markdown is avoided via the system prompt; responses come back as
  plain text suitable for a terminal Gopher client.
- If the API call fails, the user message is rolled back so conversation
  history stays consistent.
- Access log entries include the client IP, request type, selector,
  elapsed time, and status, mirroring the gopher-rss-proxy log format.
