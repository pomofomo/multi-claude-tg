// Package pubsub is an in-process fan-out: each instance has one inbound
// channel (Telegram -> Claude) and one outbound channel (Claude -> Telegram).
package pubsub

import (
	"errors"
	"sync"
)

// Inbound is a message flowing from Telegram to a Claude instance.
type Inbound struct {
	InstanceID string
	ChatID     int64
	MessageID  int
	User       string
	Text       string
	// TS is the UTC unix seconds of the original Telegram message.
	TS              int64
	AttachmentID    string // Telegram file_id if any
	AttachmentName  string
}

// Outbound is an action Claude wants to perform in Telegram.
// Kind is one of: "reply", "react", "edit", "download_attachment".
type Outbound struct {
	InstanceID string
	Kind       string
	// Fields — populated based on Kind.
	ChatID    int64
	MessageID int    // for react/edit/download
	ReplyTo   int    // for reply
	Text      string // for reply/edit
	Emoji     string // for react
	Files     []string
	// Correlation for downloads.
	ReqID string
}

// Bus is the hub.
type Bus struct {
	mu     sync.RWMutex
	inbox  map[string]chan Inbound  // InstanceID -> channel
	outbox chan Outbound
}

// New constructs a Bus. outboxBuf is the buffered size of the shared outbound channel.
func New(outboxBuf int) *Bus {
	return &Bus{
		inbox:  make(map[string]chan Inbound),
		outbox: make(chan Outbound, outboxBuf),
	}
}

// Register returns the (buffered) inbound channel for an instance, creating it on first call.
func (b *Bus) Register(instanceID string) <-chan Inbound {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.inbox[instanceID]; ok {
		return ch
	}
	ch := make(chan Inbound, 64)
	b.inbox[instanceID] = ch
	return ch
}

// Unregister closes and deletes the inbound channel for an instance.
func (b *Bus) Unregister(instanceID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.inbox[instanceID]; ok {
		close(ch)
		delete(b.inbox, instanceID)
	}
}

// SendToInstance pushes a message to the instance's inbox. Returns error if unregistered.
func (b *Bus) SendToInstance(msg Inbound) error {
	b.mu.RLock()
	ch, ok := b.inbox[msg.InstanceID]
	b.mu.RUnlock()
	if !ok {
		return errors.New("no inbox for instance " + msg.InstanceID)
	}
	select {
	case ch <- msg:
		return nil
	default:
		return errors.New("inbox full for instance " + msg.InstanceID)
	}
}

// Outbound returns the shared outbound channel. The dispatcher reads from it
// to forward actions to Telegram.
func (b *Bus) Outbound() chan Outbound { return b.outbox }
