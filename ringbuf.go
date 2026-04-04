package submux

// pendingCallbackRing is a circular buffer for pendingCallback items.
// It supports two modes: fixed-capacity (bounded, no growth) and growable
// (unlimited, doubles on full). The zero value is not ready to use; call
// initRing or let the sequencer initialize lazily on first push.
type pendingCallbackRing struct {
	buf        []pendingCallback
	head, tail int
	count      int
	fixed      bool // true = bounded (push returns false when full)
}

const ringBufInitialCap = 16

// initRing initializes the ring buffer. If capacity > 0, creates a fixed-size
// buffer (bounded mode). If capacity == 0, creates a growable buffer starting
// at ringBufInitialCap.
func (r *pendingCallbackRing) initRing(capacity int) {
	if capacity > 0 {
		r.buf = make([]pendingCallback, capacity)
		r.fixed = true
	} else {
		r.buf = make([]pendingCallback, ringBufInitialCap)
		r.fixed = false
	}
	r.head = 0
	r.tail = 0
	r.count = 0
}

// push appends an item to the back of the ring. Returns false if the buffer
// is full and fixed (bounded mode). In growable mode, doubles the buffer.
func (r *pendingCallbackRing) push(item pendingCallback) bool {
	if r.count == len(r.buf) {
		if r.fixed {
			return false
		}
		r.grow()
	}
	r.buf[r.tail] = item
	r.tail = (r.tail + 1) % len(r.buf)
	r.count++
	return true
}

// pop removes and returns the item at the front of the ring.
// Returns false if the ring is empty. The vacated slot is zeroed
// so that *Message pointers become GC-eligible.
func (r *pendingCallbackRing) pop() (pendingCallback, bool) {
	if r.count == 0 {
		return pendingCallback{}, false
	}
	item := r.buf[r.head]
	r.buf[r.head] = pendingCallback{} // zero for GC
	r.head = (r.head + 1) % len(r.buf)
	r.count--
	return item, true
}

// len returns the number of items in the ring.
func (r *pendingCallbackRing) len() int {
	return r.count
}

// grow doubles the buffer capacity, copying elements in FIFO order.
func (r *pendingCallbackRing) grow() {
	newCap := len(r.buf) * 2
	newBuf := make([]pendingCallback, newCap)
	// Copy elements in order: head..end, then 0..tail
	if r.head < r.tail {
		copy(newBuf, r.buf[r.head:r.tail])
	} else {
		n := copy(newBuf, r.buf[r.head:])
		copy(newBuf[n:], r.buf[:r.tail])
	}
	r.buf = newBuf
	r.head = 0
	r.tail = r.count
}
