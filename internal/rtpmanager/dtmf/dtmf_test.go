package dtmf

import (
	"testing"

	"github.com/pion/rtp"
)

// eventPacket builds one telephone-event RTP packet.
func eventPacket(pt uint8, code byte, end bool, durationMs int, ts uint32) *rtp.Packet {
	flags := byte(0)
	if end {
		flags = endBit
	}
	duration := durationMs * 8 // 8kHz clock
	return &rtp.Packet{
		Header:  rtp.Header{PayloadType: pt, Timestamp: ts},
		Payload: []byte{code, flags, byte(duration >> 8), byte(duration)},
	}
}

func TestDecodeDigits(t *testing.T) {
	cases := map[byte]rune{
		0: '0', 1: '1', 9: '9', 10: '*', 11: '#', 12: 'A', 15: 'D',
	}
	for code, want := range cases {
		ev, ok := Decode([]byte{code, 0, 0, 160}, 1000)
		if !ok {
			t.Fatalf("code %d should decode", code)
		}
		if ev.Digit != want {
			t.Errorf("code %d = %q, want %q", code, ev.Digit, want)
		}
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	if _, ok := Decode([]byte{1, 0, 0}, 0); ok {
		t.Error("a short payload must not decode")
	}
	if _, ok := Decode([]byte{200, 0, 0, 0}, 0); ok {
		t.Error("an unassigned event code must not decode")
	}
}

func TestDecodeReportsEndAndDuration(t *testing.T) {
	ev, ok := Decode([]byte{1, endBit, 0x03, 0x20}, 500)
	if !ok {
		t.Fatal("should decode")
	}
	if !ev.End {
		t.Error("the end bit should be reported")
	}
	if ev.DurationMs != 100 { // 800 timestamp units / 8
		t.Errorf("duration = %dms, want 100ms", ev.DurationMs)
	}
}

// One keypress is a dozen packets: an event every 20ms while the key is held,
// then the end packet three times for redundancy. Counting packets would turn
// one "1" into twelve.
func TestOneKeypressYieldsOneDigit(t *testing.T) {
	d := NewDetector(101)

	var got []rune
	// Held for 100ms, then the end packet repeated — all one event, one
	// timestamp.
	for i := 0; i < 5; i++ {
		if digit, ok := d.Handle(eventPacket(101, 1, false, i*20, 8000)); ok {
			got = append(got, digit)
		}
	}
	for i := 0; i < 3; i++ {
		if digit, ok := d.Handle(eventPacket(101, 1, true, 100, 8000)); ok {
			got = append(got, digit)
		}
	}

	if len(got) != 1 || got[0] != '1' {
		t.Fatalf("got %q, want exactly one '1'", string(got))
	}
}

// Pressing the same key twice is two events with different timestamps, and must
// yield two digits — otherwise "11" is impossible to dial.
func TestSameDigitPressedTwiceYieldsTwo(t *testing.T) {
	d := NewDetector(101)

	var got []rune
	for _, ts := range []uint32{8000, 9600} {
		for i := 0; i < 3; i++ {
			if digit, ok := d.Handle(eventPacket(101, 1, i == 2, 60, ts)); ok {
				got = append(got, digit)
			}
		}
	}

	if string(got) != "11" {
		t.Fatalf("got %q, want 11", string(got))
	}
}

// Audio and other payload types must be ignored entirely.
func TestNonEventPacketsAreIgnored(t *testing.T) {
	d := NewDetector(101)

	audio := &rtp.Packet{Header: rtp.Header{PayloadType: 0, Timestamp: 8000},
		Payload: []byte{0xFF, 0xFE, 0xFD, 0xFC}}
	if _, ok := d.Handle(audio); ok {
		t.Error("PCMU audio must not decode as DTMF")
	}
	if _, ok := d.Handle(nil); ok {
		t.Error("a nil packet must not panic or decode")
	}
}

// A leg that negotiated no telephone-event has no detector to speak of, and must
// never invent digits from whatever arrives.
func TestDisabledDetectorIgnoresEverything(t *testing.T) {
	d := NewDetector(0)

	if d.Enabled() {
		t.Fatal("payload type 0 means no DTMF transport was negotiated")
	}
	if _, ok := d.Handle(eventPacket(101, 1, true, 100, 8000)); ok {
		t.Error("a disabled detector must produce no digits")
	}
}

// --- Buffer ---

// The type-ahead case: a caller who knows the menu presses through it, and the
// digits arrive before any node is collecting.
func TestBufferHoldsTypeAhead(t *testing.T) {
	b := NewBuffer()
	b.Push('1')
	b.Push('3')

	if got := b.Buffered(); got != "13" {
		t.Fatalf("buffered = %q, want 13", got)
	}

	d, ok := b.Take()
	if !ok || d != '1' {
		t.Fatalf("first Take = %q/%v, want 1", d, ok)
	}
	d, ok = b.Take()
	if !ok || d != '3' {
		t.Fatalf("second Take = %q/%v, want 3", d, ok)
	}
	if _, ok := b.Take(); ok {
		t.Error("the buffer should now be empty")
	}
}

// A collection that starts after the digit arrived must still see it, without
// requiring another keypress.
func TestWaitSeesAlreadyBufferedDigit(t *testing.T) {
	b := NewBuffer()
	b.Push('7')

	select {
	case d := <-b.Wait():
		if d != '7' {
			t.Errorf("got %q, want 7", d)
		}
	default:
		t.Fatal("a buffered digit must satisfy Wait immediately")
	}
}

// A digit pressed while a collection is listening goes straight to it.
func TestPushWakesAWaiter(t *testing.T) {
	b := NewBuffer()
	ch := b.Wait()
	b.Push('5')

	select {
	case d := <-ch:
		if d != '5' {
			t.Errorf("got %q, want 5", d)
		}
	default:
		t.Fatal("the waiter should have received the digit")
	}
	if got := b.Buffered(); got != "" {
		t.Errorf("a digit handed to a waiter must not also be buffered, got %q", got)
	}
}

// Re-prompting after invalid input must not honour digits aimed at the old
// question.
func TestFlushDiscardsStaleDigits(t *testing.T) {
	b := NewBuffer()
	b.Push('9')
	b.Push('9')

	if dropped := b.Flush(); dropped != "99" {
		t.Errorf("Flush reported %q, want 99", dropped)
	}
	if got := b.Buffered(); got != "" {
		t.Errorf("buffer should be empty after a flush, got %q", got)
	}
}

// An abandoned collection must not leak its waiter for the life of the call.
func TestCancelReleasesAWaiter(t *testing.T) {
	b := NewBuffer()
	ch := b.Wait()
	b.Cancel(ch)

	b.Push('2')
	if got := b.Buffered(); got != "2" {
		t.Errorf("after cancelling, the digit should buffer; got %q", got)
	}
}

// A caller holding a key down must not grow the buffer without bound.
func TestBufferIsBounded(t *testing.T) {
	b := NewBuffer()
	for i := 0; i < maxBuffered*3; i++ {
		b.Push('1')
	}
	if got := len(b.Buffered()); got != maxBuffered {
		t.Errorf("buffered %d digits, want the cap of %d", got, maxBuffered)
	}
}
