package sdp

import (
	"log/slog"
	"strconv"

	"github.com/pion/sdp/v3"
)

// RTPEndpointInfo contains RTP server endpoint details
type RTPEndpointInfo struct {
	ServerAddr string
	ServerPort int
}

// telephoneEventRTPMap is the rtpmap value identifying RFC 4733 DTMF.
const telephoneEventRTPMap = "telephone-event/8000"

// BuildResponseSDP creates an SDP answer carrying a single codec.
//
// Deprecated: use BuildAnswerSDP, which can carry telephone-event alongside the
// audio codec. Kept so a caller that only knows about audio still works.
func BuildResponseSDP(serverAddr string, serverPort int, selectedCodec string) []byte {
	pt, err := strconv.Atoi(selectedCodec)
	if err != nil {
		pt = 0
	}
	return BuildAnswerSDP(serverAddr, serverPort, pt, 0, "")
}

// BuildAnswerSDP creates an SDP answer for a negotiated audio codec and,
// optionally, a telephone-event payload type.
//
// The telephone-event payload type is passed EXPLICITLY rather than looked up.
// Payload types 96-127 are dynamic, so no static table can say what one means:
// the same 96 is opus in one session and telephone-event in another. Guessing
// from a table is how an answer ends up advertising opus for a DTMF format —
// the same root cause as discarding a=rtpmap from the offer.
//
// telephoneEventPT of 0 means none was negotiated. telephoneEventFMTP echoes the
// offerer's fmtp; empty falls back to the conventional "0-15".
func BuildAnswerSDP(serverAddr string, serverPort, audioPT, telephoneEventPT int, telephoneEventFMTP string) []byte {
	rtpInfo := &RTPEndpointInfo{
		ServerAddr: serverAddr,
		ServerPort: serverPort,
	}

	return createResponseSDP(rtpInfo, audioPT, telephoneEventPT, telephoneEventFMTP)
}

// createResponseSDP creates an SDP answer.
func createResponseSDP(rtpInfo *RTPEndpointInfo, audioPT, telephoneEventPT int, telephoneEventFMTP string) []byte {
	if rtpInfo == nil {
		return nil
	}

	if audioPT < 0 {
		audioPT = 0
	}
	formats := []string{strconv.Itoa(audioPT)}
	if telephoneEventPT > 0 {
		formats = append(formats, strconv.Itoa(telephoneEventPT))
	}

	// Create a basic SDP response
	sessionDesc := &sdp.SessionDescription{
		Origin: sdp.Origin{
			Username:       "switchboard",
			SessionID:      1,
			SessionVersion: 1,
			NetworkType:    "IN",
			AddressType:    "IP4",
			UnicastAddress: rtpInfo.ServerAddr,
		},
		SessionName: "Switchboard Media Session",
		ConnectionInformation: &sdp.ConnectionInformation{
			NetworkType: "IN",
			AddressType: "IP4",
			Address: &sdp.Address{
				Address: rtpInfo.ServerAddr,
			},
		},
		TimeDescriptions: []sdp.TimeDescription{
			{
				Timing: sdp.Timing{
					StartTime: 0,
					StopTime:  0,
				},
			},
		},
		MediaDescriptions: []*sdp.MediaDescription{
			{
				MediaName: sdp.MediaName{
					Media:   "audio",
					Port:    sdp.RangedPort{Value: rtpInfo.ServerPort},
					Protos:  []string{"RTP", "AVP"},
					Formats: formats,
				},
				Attributes: getResponseAttributes(audioPT, telephoneEventPT, telephoneEventFMTP),
			},
		},
	}

	// Marshal to bytes
	sdpBytes, err := sessionDesc.Marshal()
	if err != nil {
		slog.Error("Failed to create response SDP", "error", err)
		return nil
	}

	return sdpBytes
}

// GetCodecAttributes returns rtpmap attributes for static payload types.
//
// It is only correct for STATIC types (0-95), where the number defines the
// codec. A dynamic type means whatever the session negotiated, so use
// codecAttributes, which is told rather than guessing.
func GetCodecAttributes(formats []string) []sdp.Attribute {
	attrs := []sdp.Attribute{}
	for _, format := range formats {
		if rtpmap, ok := staticRTPMap[format]; ok {
			attrs = append(attrs, sdp.Attribute{Key: "rtpmap", Value: format + " " + rtpmap})
		}
	}
	return append(attrs,
		sdp.Attribute{Key: "ptime", Value: "20"},
		sdp.Attribute{Key: "sendrecv"},
	)
}

// staticRTPMap maps STATIC payload types to their rtpmap. Dynamic types
// (96-127) are deliberately absent: they have no fixed meaning, and a table
// claiming otherwise is how an answer advertises opus for a DTMF format.
var staticRTPMap = map[string]string{
	"0":  "PCMU/8000",
	"8":  "PCMA/8000",
	"18": "G729/8000",
	"9":  "G722/8000",
	"4":  "G723/8000",
}

// codecAttributes builds the rtpmap/fmtp lines for a negotiated answer.
func codecAttributes(audioPT, telephoneEventPT int, telephoneEventFMTP string) []sdp.Attribute {
	attrs := []sdp.Attribute{}

	audio := strconv.Itoa(audioPT)
	if rtpmap, ok := staticRTPMap[audio]; ok {
		attrs = append(attrs, sdp.Attribute{Key: "rtpmap", Value: audio + " " + rtpmap})
	}

	if telephoneEventPT > 0 {
		pt := strconv.Itoa(telephoneEventPT)
		attrs = append(attrs, sdp.Attribute{Key: "rtpmap", Value: pt + " " + telephoneEventRTPMap})

		params := telephoneEventFMTP
		if params == "" {
			params = "0-15"
		}
		attrs = append(attrs, sdp.Attribute{Key: "fmtp", Value: pt + " " + params})
	}

	// 20ms frames, the VoIP standard.
	attrs = append(attrs, sdp.Attribute{Key: "ptime", Value: "20"})
	attrs = append(attrs, sdp.Attribute{Key: "sendrecv"})

	return attrs
}

// getResponseAttributes returns attributes for SDP response (includes rtcp-mux)
func getResponseAttributes(audioPT, telephoneEventPT int, telephoneEventFMTP string) []sdp.Attribute {
	attrs := codecAttributes(audioPT, telephoneEventPT, telephoneEventFMTP)

	// Add rtcp-mux (RFC 5761) - means RTCP is on same port as RTP
	attrs = append(attrs, sdp.Attribute{
		Key: "rtcp-mux",
	})

	return attrs
}
