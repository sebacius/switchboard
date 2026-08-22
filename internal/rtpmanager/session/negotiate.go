package session

import (
	"fmt"
	"strconv"
	"strings"
)

// Codec negotiation lives here rather than being written twice, once for the
// A-leg and once for the pending-remote B-leg. The two copies had already
// drifted into a literal `if codec == "0"` each; adding telephone-event to both
// would have doubled the chance of them disagreeing.

// telephoneEventEncoding is the rtpmap name for RFC 4733 DTMF.
const telephoneEventEncoding = "telephone-event"

// pcmuPayloadType is the static payload type for G.711 µ-law, the one audio
// codec this system carries.
const pcmuPayloadType = 0

// CodecOffer is one offered format with the rtpmap that gives it meaning.
type CodecOffer struct {
	PayloadType  int
	EncodingName string
	ClockRate    int
	FMTP         string
}

// AnswerSpec is what negotiation decided: the audio codec, and the DTMF payload
// type if one was offered.
type AnswerSpec struct {
	// AudioPT is the negotiated audio payload type.
	AudioPT int
	// TelephoneEventPT is the negotiated RFC 4733 payload type, or 0 when the
	// offer carried none.
	TelephoneEventPT int
	// TelephoneEventFMTP is the offerer's fmtp, echoed back. Empty means the
	// conventional "0-15".
	TelephoneEventFMTP string
}

// HasDTMF reports whether this leg negotiated an RFC 4733 transport.
func (a AnswerSpec) HasDTMF() bool { return a.TelephoneEventPT > 0 }

// Formats returns the payload types for the answer's m= line, audio first.
func (a AnswerSpec) Formats() []string {
	out := []string{strconv.Itoa(a.AudioPT)}
	if a.HasDTMF() {
		out = append(out, strconv.Itoa(a.TelephoneEventPT))
	}
	return out
}

// Negotiate picks the answer from an offer.
//
// Two rules matter beyond "we only speak PCMU":
//
// The telephone-event payload type is ECHOED, never assumed. It is a dynamic
// type, so real endpoints offer it anywhere in 96-127; hard-coding 101 works
// against a softphone that happens to use 101 and silently fails against
// everything else.
//
// An offer without telephone-event yields an answer without it. An answer may
// only contain formats from the offer, and inventing one would claim a DTMF
// transport the peer never agreed to — which then fails at the only moment it
// matters, when a caller presses a digit.
func Negotiate(offers []CodecOffer, legacyCodecs []string) (AnswerSpec, error) {
	spec := AnswerSpec{AudioPT: -1}

	for _, o := range offers {
		// PCMU is static: payload type 0 defines it whether or not an rtpmap
		// says so, and some endpoints omit the rtpmap for static types.
		if o.PayloadType == pcmuPayloadType &&
			(o.EncodingName == "" || strings.EqualFold(o.EncodingName, "PCMU")) {
			if spec.AudioPT < 0 {
				spec.AudioPT = pcmuPayloadType
			}
			continue
		}
		if strings.EqualFold(o.EncodingName, telephoneEventEncoding) && spec.TelephoneEventPT == 0 {
			spec.TelephoneEventPT = o.PayloadType
			spec.TelephoneEventFMTP = o.FMTP
		}
	}

	// Fall back to the bare payload-type list when the offer carried no rtpmap
	// detail, so an older signaling server still negotiates audio.
	if spec.AudioPT < 0 {
		for _, codec := range legacyCodecs {
			if strings.TrimSpace(codec) == "0" {
				spec.AudioPT = pcmuPayloadType
				break
			}
		}
	}

	if spec.AudioPT < 0 {
		return AnswerSpec{}, fmt.Errorf("no supported codec offered (PCMU required)")
	}
	return spec, nil
}
