// Package ws is the dispatcher's WebSocket server. Channel plugins connect
// here with their instance_id + secret and bidirectionally bridge messages.
//
// Wire protocol (JSON frames, one per WS message):
//
//   Server -> plugin:
//     {"type": "message", "chat_id": 1, "message_id": 2, "thread_id": 3,
//      "user": "alice", "ts": 1700000000, "text": "...",
//      "attachment_file_id": "...", "attachment_name": "..."}
//
//   Plugin -> server:
//     {"type": "reply",   "chat_id": 1, "reply_to": 2, "text": "...", "files": ["/abs/path"]}
//     {"type": "react",   "chat_id": 1, "message_id": 2, "emoji": "👍"}
//     {"type": "edit",    "chat_id": 1, "message_id": 2, "text": "..."}
//     {"type": "download","chat_id": 1, "file_id": "...", "req_id": "abc"}
//     {"type": "hello",   "instance_id": "..."}  (optional, informational)
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Handler is the interface the dispatcher implements to react to plugin events.
type Handler interface {
	// AuthSecret looks up an instance by secret; returns (instance_id, chat_id, thread_id, ok).
	AuthSecret(secret string) (instanceID string, chatID int64, threadID int, ok bool)
	// OnOutbound handles a plugin->server message.
	OnOutbound(instanceID string, frame Frame)
	// Register registers the conn as the live link for an instance. Returns the inbound
	// channel the WS writer should drain from.
	Register(instanceID string, conn *Conn) <-chan Frame
	// Unregister clears the live link when the conn drops.
	Unregister(instanceID string, conn *Conn)
	// ListInstances returns JSON-encoded instance list for the CLI API.
	ListInstances() ([]byte, error)
}

// Frame is one JSON frame across the WS.
type Frame struct {
	Type      string `json:"type"`
	ChatID    int64  `json:"chat_id,omitempty"`
	MessageID int    `json:"message_id,omitempty"`
	ThreadID  int    `json:"thread_id,omitempty"`
	ReplyTo   int    `json:"reply_to,omitempty"`
	User      string `json:"user,omitempty"`
	Text      string `json:"text,omitempty"`
	TS        int64  `json:"ts,omitempty"`
	Emoji     string `json:"emoji,omitempty"`
	Files     []string `json:"files,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	ReqID     string `json:"req_id,omitempty"`
	Path      string `json:"path,omitempty"` // download response: local file path
	Err       string `json:"error,omitempty"`
	AttachmentFileID string `json:"attachment_file_id,omitempty"`
	AttachmentName   string `json:"attachment_name,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
}

// Conn wraps a live WebSocket and serializes writes.
type Conn struct {
	mu   sync.Mutex
	ws   wsWriter
	done chan struct{}
}

// wsWriter is the minimum we need. Implemented by coder/websocket.Conn.
type wsWriter interface {
	Write(ctx context.Context, typ int, data []byte) error
	Close(code int, reason string) error
}

// Send writes a frame. Safe for concurrent callers.
func (c *Conn) Send(frame Frame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.ws.Write(ctx, 1 /* text */, data)
}

// Server serves the dispatcher's HTTP/WS endpoints.
type Server struct {
	addr   string
	logger *slog.Logger
	h      Handler
	srv    *http.Server
	// swappable so the package can be built without coder/websocket in tests.
	upgradeAndServe func(w http.ResponseWriter, r *http.Request, s *Server) error
}

// New constructs a Server bound to addr (e.g. "127.0.0.1:7777").
func New(addr string, logger *slog.Logger, h Handler) *Server {
	return &Server{
		addr:            addr,
		logger:          logger,
		h:               h,
		upgradeAndServe: upgradeAndServeCoder,
	}
}

// ListenAndServe starts the HTTP listener. Blocks until Shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/instances", s.handleAPIInstances)
	mux.HandleFunc("/channel", s.handleChannel)
	s.srv = &http.Server{Addr: s.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// handleAPIInstances returns the instance list as JSON for CLI commands.
func (s *Server) handleAPIInstances(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := s.h.ListInstances()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// handleChannel validates ?secret= and upgrades to WebSocket.
func (s *Server) handleChannel(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("secret")
	if secret == "" {
		http.Error(w, "missing secret", http.StatusUnauthorized)
		return
	}
	instanceID, _, _, ok := s.h.AuthSecret(secret)
	if !ok {
		http.Error(w, "invalid secret", http.StatusUnauthorized)
		return
	}
	// Store instanceID for the upgrader.
	r = r.WithContext(context.WithValue(r.Context(), ctxInstanceID{}, instanceID))
	if err := s.upgradeAndServe(w, r, s); err != nil {
		s.logger.Error("upgrade failed", "err", err)
	}
}

type ctxInstanceID struct{}

// instanceIDFromCtx returns the instanceID the handler stashed.
func instanceIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxInstanceID{}).(string)
	return v
}

// --- coder/websocket integration is in a separate file so tests can stub it. ---

// serveConn drains inbound frames from the dispatcher (server->plugin) and
// reads plugin->server frames. Returns when the ws closes.
func (s *Server) serveConn(ctx context.Context, conn *Conn, instanceID string, read func(context.Context) ([]byte, error)) {
	inbound := s.h.Register(instanceID, conn)
	defer s.h.Unregister(instanceID, conn)

	// Writer loop
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-inbound:
				if !ok {
					return
				}
				if err := conn.Send(frame); err != nil {
					s.logger.Warn("ws write failed", "instance", instanceID, "err", err)
					return
				}
			}
		}
	}()

	// Reader loop
	for {
		data, err := read(ctx)
		if err != nil {
			return
		}
		var frame Frame
		if err := json.Unmarshal(data, &frame); err != nil {
			s.logger.Warn("bad frame", "instance", instanceID, "err", err)
			continue
		}
		s.h.OnOutbound(instanceID, frame)
	}
}
