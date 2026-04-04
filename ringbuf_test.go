package submux

import (
	"testing"
	"time"
)

func TestPendingCallbackRing_PushPop_FIFO(t *testing.T) {
	var r pendingCallbackRing
	r.initRing(0) // growable

	const n = 10
	for i := range n {
		msg := &Message{Payload: string(rune('a' + i))}
		if !r.push(pendingCallback{msg: msg}) {
			t.Fatalf("push %d failed", i)
		}
	}

	if r.len() != n {
		t.Fatalf("len: got %d, want %d", r.len(), n)
	}

	for i := range n {
		item, ok := r.pop()
		if !ok {
			t.Fatalf("pop %d failed", i)
		}
		want := string(rune('a' + i))
		if item.msg.Payload != want {
			t.Fatalf("pop %d: got %q, want %q", i, item.msg.Payload, want)
		}
	}

	if r.len() != 0 {
		t.Fatalf("len after drain: got %d, want 0", r.len())
	}
}

func TestPendingCallbackRing_Wraparound(t *testing.T) {
	var r pendingCallbackRing
	r.initRing(4) // fixed, capacity 4

	// Fill halfway, drain, fill again to force wraparound
	for i := range 2 {
		r.push(pendingCallback{msg: &Message{Payload: "first"}, enqueuedAt: time.Now()})
		_ = i
	}
	r.pop()
	r.pop()
	// head is now at 2, tail at 2, count 0

	// Push 4 items — tail wraps around from 2 → 3 → 0 → 1
	for i := range 4 {
		msg := &Message{SubscriptionType: subscriptionType(i)}
		if !r.push(pendingCallback{msg: msg}) {
			t.Fatalf("push %d failed after wraparound", i)
		}
	}

	if r.len() != 4 {
		t.Fatalf("len: got %d, want 4", r.len())
	}

	// Verify FIFO order
	for i := range 4 {
		item, ok := r.pop()
		if !ok {
			t.Fatalf("pop %d failed", i)
		}
		if int(item.msg.SubscriptionType) != i {
			t.Fatalf("pop %d: got %d, want %d", i, item.msg.SubscriptionType, i)
		}
	}
}

func TestPendingCallbackRing_GrowUnbounded(t *testing.T) {
	var r pendingCallbackRing
	r.initRing(0) // growable, starts at ringBufInitialCap (16)

	initialCap := len(r.buf)
	if initialCap != ringBufInitialCap {
		t.Fatalf("initial cap: got %d, want %d", initialCap, ringBufInitialCap)
	}

	// Push more than initial capacity to trigger growth
	const n = 50
	for i := range n {
		msg := &Message{SubscriptionType: subscriptionType(i)}
		if !r.push(pendingCallback{msg: msg}) {
			t.Fatalf("push %d failed", i)
		}
	}

	if r.len() != n {
		t.Fatalf("len: got %d, want %d", r.len(), n)
	}

	if len(r.buf) <= initialCap {
		t.Fatalf("buffer should have grown beyond %d, got %d", initialCap, len(r.buf))
	}

	// Verify FIFO order is preserved after growth
	for i := range n {
		item, ok := r.pop()
		if !ok {
			t.Fatalf("pop %d failed", i)
		}
		if int(item.msg.SubscriptionType) != i {
			t.Fatalf("pop %d: got %d, want %d", i, item.msg.SubscriptionType, i)
		}
	}
}

func TestPendingCallbackRing_FixedFull(t *testing.T) {
	var r pendingCallbackRing
	r.initRing(3) // fixed capacity 3

	for range 3 {
		if !r.push(pendingCallback{msg: &Message{}}) {
			t.Fatal("push to non-full ring failed")
		}
	}

	// Fourth push should fail
	if r.push(pendingCallback{msg: &Message{}}) {
		t.Fatal("push to full fixed ring should return false")
	}

	if r.len() != 3 {
		t.Fatalf("len: got %d, want 3", r.len())
	}
}

func TestPendingCallbackRing_PopEmpty(t *testing.T) {
	var r pendingCallbackRing
	r.initRing(4)

	_, ok := r.pop()
	if ok {
		t.Fatal("pop on empty ring should return false")
	}

	// Push and drain, then pop again
	r.push(pendingCallback{msg: &Message{}})
	r.pop()

	_, ok = r.pop()
	if ok {
		t.Fatal("pop on drained ring should return false")
	}
}

func TestPendingCallbackRing_ZeroForGC(t *testing.T) {
	var r pendingCallbackRing
	r.initRing(4)

	msg := &Message{Payload: "test"}
	r.push(pendingCallback{msg: msg})

	// The slot at head (0) holds our message
	if r.buf[0].msg == nil {
		t.Fatal("slot should hold message before pop")
	}

	r.pop()

	// After pop, the slot should be zeroed
	if r.buf[0].msg != nil {
		t.Fatal("slot should be zeroed after pop")
	}
}
