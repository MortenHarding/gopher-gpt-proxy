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
  -host string   Hostname clients use to reach this server (default "localhost")
  -port string   TCP port to listen on (default "7070")
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
Build the image, with your own groq api key

```bash
docker build -t gopher-gpt-proxy .
```

Run the docker container

```bash
docker run -e GROQ_API_KEY="gsk_..." -p 7070:7070 gopher-gpt-proxy .
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
