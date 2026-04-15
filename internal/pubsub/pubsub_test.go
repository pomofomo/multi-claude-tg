package pubsub

import (
	"testing"
	"time"
)

func TestRegisterSendReceive(t *testing.T) {
	b := New(8)
	ch := b.Register("inst-1")

	err := b.SendToInstance(Inbound{InstanceID: "inst-1", Text: "hi"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case m := <-ch:
		if m.Text != "hi" {
			t.Errorf("want text=hi, got %q", m.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestSendToMissingInstanceErrors(t *testing.T) {
	b := New(8)
	err := b.SendToInstance(Inbound{InstanceID: "nope"})
	if err == nil {
		t.Error("want error for missing instance")
	}
}

func TestUnregisterClosesChannel(t *testing.T) {
	b := New(8)
	ch := b.Register("x")
	b.Unregister("x")
	if _, ok := <-ch; ok {
		t.Error("channel should be closed")
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	b := New(8)
	c1 := b.Register("id")
	c2 := b.Register("id")
	// same underlying channel — receive should work through either reference.
	_ = b.SendToInstance(Inbound{InstanceID: "id"})
	select {
	case <-c1:
	case <-c2:
	case <-time.After(time.Second):
		t.Fatal("no delivery")
	}
}
