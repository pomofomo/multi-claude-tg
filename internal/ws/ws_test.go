package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeWriter records writes for assertion.
type fakeWriter struct {
	mu     sync.Mutex
	frames []Frame
	closed bool
}

func (f *fakeWriter) Write(_ context.Context, _ int, data []byte) error {
	var fr Frame
	_ = json.Unmarshal(data, &fr)
	f.mu.Lock()
	f.frames = append(f.frames, fr)
	f.mu.Unlock()
	return nil
}

func (f *fakeWriter) Close(_ int, _ string) error {
	f.closed = true
	return nil
}

type fakeHandler struct {
	mu         sync.Mutex
	registered map[string]bool
	outbound   []Frame
	inbound    map[string]chan Frame
}

func newFakeHandler() *fakeHandler {
	return &fakeHandler{
		registered: map[string]bool{},
		inbound:    map[string]chan Frame{},
	}
}

func (h *fakeHandler) AuthSecret(secret string) (string, int64, int, bool) {
	if secret == "good" {
		return "inst-1", 123, 1, true
	}
	return "", 0, 0, false
}

func (h *fakeHandler) OnOutbound(instanceID string, frame Frame) {
	h.mu.Lock()
	h.outbound = append(h.outbound, frame)
	h.mu.Unlock()
}

func (h *fakeHandler) Register(instanceID string, _ *Conn) <-chan Frame {
	ch := make(chan Frame, 16)
	h.mu.Lock()
	h.registered[instanceID] = true
	h.inbound[instanceID] = ch
	h.mu.Unlock()
	return ch
}

func (h *fakeHandler) Unregister(instanceID string, _ *Conn) {
	h.mu.Lock()
	delete(h.registered, instanceID)
	if ch, ok := h.inbound[instanceID]; ok {
		close(ch)
		delete(h.inbound, instanceID)
	}
	h.mu.Unlock()
}

func (h *fakeHandler) ListInstances() ([]byte, error) {
	return []byte("[]"), nil
}

func (h *fakeHandler) AllowedUsers() ([]string, error)  { return nil, nil }
func (h *fakeHandler) AddAllowedUser(_ string) error    { return nil }
func (h *fakeHandler) RemoveAllowedUser(_ string) error { return nil }

func TestHandleChannelRejectsNoSecret(t *testing.T) {
	h := newFakeHandler()
	s := New("127.0.0.1:0", nil, h)
	req := httptest.NewRequest(http.MethodGet, "/channel", nil)
	w := httptest.NewRecorder()
	s.handleChannel(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandleChannelRejectsBadSecret(t *testing.T) {
	h := newFakeHandler()
	s := New("127.0.0.1:0", nil, h)
	req := httptest.NewRequest(http.MethodGet, "/channel?secret=bad", nil)
	w := httptest.NewRecorder()
	s.handleChannel(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestServeConnReaderWriter(t *testing.T) {
	h := newFakeHandler()
	s := New("127.0.0.1:0", nil, h)
	fw := &fakeWriter{}
	conn := &Conn{ws: fw, done: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())

	// Simulate a reader that delivers one frame then closes.
	frames := [][]byte{
		mustJSON(Frame{Type: "reply", Text: "hello"}),
	}
	idx := 0
	read := func(_ context.Context) ([]byte, error) {
		if idx < len(frames) {
			data := frames[idx]
			idx++
			return data, nil
		}
		// Wait for context to cancel.
		<-ctx.Done()
		return nil, ctx.Err()
	}

	done := make(chan struct{})
	go func() {
		s.serveConn(ctx, conn, "inst-1", read)
		close(done)
	}()

	// Give the goroutine a moment to process.
	time.Sleep(50 * time.Millisecond)

	// Send a frame inbound (server->plugin).
	h.mu.Lock()
	ch := h.inbound["inst-1"]
	h.mu.Unlock()
	if ch != nil {
		ch <- Frame{Type: "message", Text: "from server"}
	}
	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	// Check outbound frame was delivered to handler.
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.outbound) != 1 || h.outbound[0].Type != "reply" {
		t.Errorf("expected 1 outbound reply frame, got %+v", h.outbound)
	}

	// Check inbound frame was written to the conn.
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.frames) != 1 || fw.frames[0].Type != "message" {
		t.Errorf("expected 1 inbound message frame, got %+v", fw.frames)
	}
}

func TestConnSend(t *testing.T) {
	fw := &fakeWriter{}
	c := &Conn{ws: fw, done: make(chan struct{})}
	if err := c.Send(Frame{Type: "test", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.frames) != 1 || fw.frames[0].Text != "hi" {
		t.Errorf("unexpected frames: %+v", fw.frames)
	}
}

func mustJSON(f Frame) []byte {
	data, _ := json.Marshal(f)
	return data
}
