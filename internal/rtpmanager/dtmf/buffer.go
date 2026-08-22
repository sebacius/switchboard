package dtmf

import (
	"strings"
	"sync"
)

// maxBuffered bounds the digit buffer. A caller cannot usefully type ahead by
// more than a few digits, and an unbounded buffer is a memory leak wearing a
// feature's clothes.
const maxBuffered = 32

// Buffer holds the digits a caller has pressed but no node has consumed yet.
//
// It exists because of type-ahead. A caller who knows the menu presses "1" then
// "3" without waiting for either prompt, and those digits arrive while one node
// is still playing and before the next has started collecting. Without a buffer
// spanning the gap, the second digit lands in no collection at all and the
// caller is told their entry was invalid — the single most common complaint
// about IVR systems, and entirely self-inflicted.
//
// So digits are captured continuously from the moment the session exists, and a
// collection drains what is waiting before it listens for more.
type Buffer struct {
	mu      sync.Mutex
	digits  []rune
	waiters []chan rune
}

// NewBuffer builds an empty buffer.
func NewBuffer() *Buffer { return &Buffer{} }

// Push records a pressed digit, waking one waiter if a collection is listening.
func (b *Buffer) Push(digit rune) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Hand it straight to a waiting collection rather than buffering it.
	for len(b.waiters) > 0 {
		w := b.waiters[0]
		b.waiters = b.waiters[1:]
		select {
		case w <- digit:
			return
		default:
			// That waiter has given up; try the next.
		}
	}

	if len(b.digits) >= maxBuffered {
		// Drop the oldest: a caller mashing keys cares about what they pressed
		// most recently, and the alternative is unbounded growth.
		b.digits = b.digits[1:]
	}
	b.digits = append(b.digits, digit)
}

// Take removes and returns one buffered digit, or ok=false when none is waiting.
func (b *Buffer) Take() (rune, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.digits) == 0 {
		return 0, false
	}
	d := b.digits[0]
	b.digits = b.digits[1:]
	return d, true
}

// Wait returns a channel that receives the next digit. The caller must always
// release it with Cancel, or a waiter leaks for the life of the session.
func (b *Buffer) Wait() chan rune {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan rune, 1)

	// A digit that arrived first should not require a further keypress to be
	// noticed.
	if len(b.digits) > 0 {
		ch <- b.digits[0]
		b.digits = b.digits[1:]
		return ch
	}

	b.waiters = append(b.waiters, ch)
	return ch
}

// Cancel releases a channel returned by Wait.
func (b *Buffer) Cancel(ch chan rune) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, w := range b.waiters {
		if w == ch {
			b.waiters = append(b.waiters[:i], b.waiters[i+1:]...)
			return
		}
	}
}

// Flush discards everything buffered, returning what it dropped.
//
// A node re-prompting after invalid input flushes: the caller's earlier digits
// were a response to a question that has now changed, and honoring them would
// answer the new prompt with the old answer.
func (b *Buffer) Flush() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	dropped := string(b.digits)
	b.digits = nil
	return dropped
}

// Buffered returns the digits currently waiting, for logs and tests.
func (b *Buffer) Buffered() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.digits)
}

// Contains reports whether a digit is in a set such as a terminator list.
func Contains(set string, digit rune) bool {
	return set != "" && strings.ContainsRune(set, digit)
}
