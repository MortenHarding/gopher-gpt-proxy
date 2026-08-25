package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ── Configuration ─────────────────────────────────────────────────────────────

const (
	gopherPort   = ":7070" // Use 7070 so we don't need root; change to :70 in production
	sessionTTL   = 30 * time.Minute
	maxLineWidth = 70 // Classic Gopher line-width convention

	// Groq — OpenAI-compatible, free tier, requires GROQ_API_KEY.
	// Browse available models at: https://console.groq.com/docs/models
	groqBaseURL = "https://api.groq.com/openai/v1/chat/completions"
	groqModel   = "openai/gpt-oss-120b" // was: llama-3.3-70b-versatile

	systemPrompt = "You are a helpful assistant. Keep responses concise and suitable for a plain-text terminal. Avoid markdown formatting."
)

// ── Session store ─────────────────────────────────────────────────────────────

// Message represents a single turn in a conversation.
// Roles follow the OpenAI convention: "system", "user", "assistant".
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Session struct {
	History  []Message
	LastSeen time.Time
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionStore() *SessionStore {
	s := &SessionStore{sessions: make(map[string]*Session)}
	go s.reap()
	return s
}

func (s *SessionStore) GetOrCreate(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		sess = &Session{
			History: []Message{
				{Role: "system", Content: systemPrompt},
			},
		}
		s.sessions[id] = sess
	}
	sess.LastSeen = time.Now()
	return sess
}

func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *SessionStore) reap() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		s.mu.Lock()
		for id, sess := range s.sessions {
			if time.Since(sess.LastSeen) > sessionTTL {
				log.Printf("session expired: %s", id)
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
	}
}

// ── Access Logger ─────────────────────────────────────────────────────────────

// AccessLogger writes structured tab-separated access log lines to a file.
type AccessLogger struct {
	logger *log.Logger
}

// LogEntry holds all fields for a single access log line.
type LogEntry struct {
	Timestamp time.Time
	IP        string
	ItemType  string // "menu", "chat", "new", "history", "error"
	Selector  string
	ElapsedMS int64
	Status    string // "ok" or "error: <reason>"
}

// NewAccessLogger opens (or creates) path for append-only writing.
// Returns nil, nil if path is empty (logging disabled).
func NewAccessLogger(path string) (*AccessLogger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening access log %q: %w", path, err)
	}
	return &AccessLogger{
		logger: log.New(f, "", 0),
	}, nil
}

// Log writes a single access log entry. Safe to call on a nil receiver
// (logging disabled).
func (l *AccessLogger) Log(e LogEntry) {
	if l == nil {
		return
	}
	l.logger.Printf("%s\t%s\t%-7s\t%s\t%dms\t%s",
		e.Timestamp.UTC().Format(time.RFC3339),
		e.IP,
		e.ItemType,
		e.Selector,
		e.ElapsedMS,
		e.Status,
	)
}

// itemType classifies a GopherGPT selector into a human-readable label.
func itemType(selector string) string {
	if selector == "" || selector == "/" {
		return "menu"
	}
	parts := strings.Split(strings.TrimPrefix(selector, "/"), "/")
	if len(parts) == 0 {
		return "menu"
	}
	switch parts[0] {
	case "chat":
		return "chat"
	case "new":
		return "new"
	case "history":
		return "history"
	default:
		return "unknown"
	}
}

// ── Groq / OpenAI-compatible client ──────────────────────────────────────────

type groqRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type groqResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

// callGroq sends the full conversation history and returns the assistant reply.
func callGroq(apiKey string, history []Message) (string, error) {
	body, err := json.Marshal(groqRequest{Model: groqModel, Messages: history})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", groqBaseURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	log.Printf("groq HTTP %d body: %s", resp.StatusCode, string(raw))

	if resp.StatusCode == 429 {
		return "", fmt.Errorf("rate limit exceeded — wait a moment and try again")
	}

	var gr groqResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return "", fmt.Errorf("json decode: %w", err)
	}
	if gr.Error != nil {
		return "", fmt.Errorf("groq error: %v", gr.Error.Message)
	}
	if len(gr.Choices) == 0 {
		return "", fmt.Errorf("empty response from Groq (HTTP %d)", resp.StatusCode)
	}
	return gr.Choices[0].Message.Content, nil
}

// ── Gopher response helpers ───────────────────────────────────────────────────

func gopherInfo(w io.Writer, text string) {
	fmt.Fprintf(w, "i%s\tfake\t(NULL)\t0\r\n", text)
}

func gopherSearch(w io.Writer, display, selector, host, port string) {
	fmt.Fprintf(w, "7%s\t%s\t%s\t%s\r\n", display, selector, host, port)
}

func gopherEnd(w io.Writer) {
	fmt.Fprint(w, ".\r\n")
}

func wrapText(text string, width int) []string {
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if len(line)+1+len(word) > width {
				lines = append(lines, line)
				line = word
			} else {
				line += " " + word
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func sessionIDFromConn(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

func sessionIDFromSelector(selector string) string {
	parts := strings.Split(strings.TrimPrefix(selector, "/"), "/")
	if len(parts) >= 2 && parts[1] != "" {
		return parts[1]
	}
	return ""
}

// ── Request handler ───────────────────────────────────────────────────────────

type Server struct {
	store     *SessionStore
	apiKey    string
	host      string
	port      string
	accessLog *AccessLogger
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(60 * time.Second))

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		log.Printf("error read: %v", err)
		return
	}
	line = strings.TrimRight(line, "\r\n")

	var selector, query string
	if idx := strings.Index(line, "\t"); idx >= 0 {
		selector = line[:idx]
		query = strings.TrimSpace(line[idx+1:])
	} else {
		selector = line
	}

	ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	log.Printf("request selector=%q query=%q from=%s", selector, query, conn.RemoteAddr())

	start := time.Now()

	sessID := sessionIDFromSelector(selector)
	if sessID == "" {
		sessID = sessionIDFromConn(conn)
	}

	command := "/"
	parts := strings.Split(strings.TrimPrefix(selector, "/"), "/")
	if len(parts) > 0 && parts[0] != "" {
		command = parts[0]
	}

	var routeErr error
	switch command {
	case "/", "":
		s.serveMenu(conn, sessID)
	case "chat":
		routeErr = s.serveChat(conn, sessID, query)
	case "new":
		s.serveNew(conn, sessID)
	case "history":
		s.serveHistory(conn, sessID)
	default:
		gopherInfo(conn, "Unknown selector. Please start at the root menu.")
		gopherEnd(conn)
	}

	elapsed := time.Since(start).Milliseconds()

	// ── access log ──────────────────────────────────────────────────────────
	entry := LogEntry{
		Timestamp: time.Now().UTC(),
		IP:        ip,
		ItemType:  itemType(selector),
		Selector:  selector,
		ElapsedMS: elapsed,
		Status:    "ok",
	}
	if routeErr != nil {
		entry.Status = "error: " + routeErr.Error()
		entry.ItemType = "error"
	}
	s.accessLog.Log(entry)
	// ────────────────────────────────────────────────────────────────────────
}

func (s *Server) serveMenu(conn net.Conn, sessID string) {
	gopherInfo(conn, " __                    ")
	gopherInfo(conn, "/ _   _   _  |_   _  _ ")
	gopherInfo(conn, "\\__) (_) |_) | ) (- | ")
	gopherInfo(conn, "         |         GPT/Groq")
	gopherInfo(conn, "_______________________________")
	gopherInfo(conn, "")
	gopherInfo(conn, "Chat with an LLM via Groq.")
	gopherInfo(conn, "")
	gopherSearch(conn, "Chat", "/chat", s.host, s.port)
	gopherSearch(conn, "New session/clear history", "/new", s.host, s.port)
	fmt.Fprintf(conn, "1View history\t/history\t%s\t%s\r\n", s.host, s.port)
	gopherInfo(conn, "")
	gopherInfo(conn, "History saved for 30 minutes.")
	gopherEnd(conn)
}

// serveChat returns an error only when the Groq API call fails, so the
// access logger can record it.  All other failure paths write their own
// Gopher error lines and return nil.
func (s *Server) serveChat(conn net.Conn, sessID, userMsg string) error {
	if userMsg == "" {
		gopherInfo(conn, "Type your message and press Enter.")
		gopherSearch(conn, "Send message", "/chat", s.host, s.port)
		gopherEnd(conn)
		return nil
	}

	sess := s.store.GetOrCreate(sessID)
	sess.History = append(sess.History, Message{Role: "user", Content: userMsg})

	gopherInfo(conn, "")
	gopherInfo(conn, "You: "+userMsg)
	gopherInfo(conn, strings.Repeat("-", maxLineWidth))
	gopherInfo(conn, "Thinking...")

	reply, err := callGroq(s.apiKey, sess.History)
	if err != nil {
		log.Printf("groq error %v", err)
		// Roll back the user message so history stays consistent.
		sess.History = sess.History[:len(sess.History)-1]
		gopherInfo(conn, "Error: "+err.Error())
		gopherEnd(conn)
		return err
	}

	sess.History = append(sess.History, Message{Role: "assistant", Content: reply})

	gopherInfo(conn, "")
	gopherInfo(conn, "Assistant:")
	gopherInfo(conn, strings.Repeat("-", maxLineWidth))
	for _, l := range wrapText(reply, maxLineWidth) {
		gopherInfo(conn, l)
	}
	gopherInfo(conn, strings.Repeat("-", maxLineWidth))
	gopherInfo(conn, fmt.Sprintf("Turns so far: %d", (len(sess.History)-1)/2))
	gopherInfo(conn, "")

	gopherSearch(conn, "Reply", "/chat", s.host, s.port)
	gopherSearch(conn, "Start a new conversation", "/new", s.host, s.port)
	fmt.Fprintf(conn, "1View history\t/history\t%s\t%s\r\n", s.host, s.port)
	gopherEnd(conn)
	return nil
}

func (s *Server) serveNew(conn net.Conn, sessID string) {
	s.store.Delete(sessID)
	gopherInfo(conn, "Session cleared. Starting fresh!")
	gopherInfo(conn, "")
	gopherSearch(conn, "Start chatting", "/chat", s.host, s.port)
	gopherEnd(conn)
}

func (s *Server) serveHistory(conn net.Conn, sessID string) {
	sess := s.store.GetOrCreate(sessID)

	gopherInfo(conn, " Conversation History")
	gopherInfo(conn, "_______________________________")
	gopherInfo(conn, "")

	turns := 0
	for _, msg := range sess.History {
		if msg.Role == "system" {
			continue
		}
		label := "You"
		if msg.Role == "assistant" {
			label = "Assistant"
		}
		gopherInfo(conn, fmt.Sprintf("-- %s --", label))
		for _, l := range wrapText(msg.Content, maxLineWidth) {
			gopherInfo(conn, l)
		}
		gopherInfo(conn, "")
		turns++
	}

	if turns == 0 {
		gopherInfo(conn, "(No messages yet.)")
		gopherInfo(conn, "")
	}

	gopherSearch(conn, "Back to chat", "/chat", s.host, s.port)
	gopherEnd(conn)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	flagHost := flag.String("host", "localhost", "Hostname clients use to reach this server")
	flagPort := flag.String("port", "7070", "TCP port to listen on (use 70 in production)")
	flagLog := flag.String("access-log", "access.log", "Path to access log file (empty to disable)")
	flag.Parse()

	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		log.Fatal("GROQ_API_KEY environment variable is not set — get a free key at https://console.groq.com")
	}

	var al *AccessLogger
	if *flagLog != "" {
		var err error
		al, err = NewAccessLogger(*flagLog)
		if err != nil {
			log.Fatalf("access log error: %v", err)
		}
		log.Printf("Access log: %s", *flagLog)
	}

	listenAddr := ":" + *flagPort

	srv := &Server{
		store:     NewSessionStore(),
		apiKey:    apiKey,
		host:      *flagHost,
		port:      *flagPort,
		accessLog: al,
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}
	log.Printf("GopherGPT listening on %s (host=%s, model=%s)", listenAddr, *flagHost, groqModel)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept error %v", err)
			continue
		}
		go srv.handle(conn)
	}
}
