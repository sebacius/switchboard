package sdp

import (
	"log/slog"

	"github.com/pion/sdp/v3"
)

// RTPEndpointInfo contains RTP server endpoint details
type RTPEndpointInfo struct {
	ServerAddr string
	ServerPort int
}

// BuildResponseSDP creates an SDP response for media sessions with the selected codec
func BuildResponseSDP(serverAddr string, serverPort int, selectedCodec string) []byte {
	rtpInfo := &RTPEndpointInfo{
		ServerAddr: serverAddr,
		ServerPort: serverPort,
	}

	return createResponseSDP(rtpInfo, selectedCodec)
}

// createResponseSDP creates an SDP response with the selected codec
func createResponseSDP(rtpInfo *RTPEndpointInfo, selectedCodec string) []byte {
	if rtpInfo == nil {
		return nil
	}

	// Use the selected codec (default to PCMU if empty)
	if selectedCodec == "" {
		selectedCodec = "0"
	}
	formats := []string{selectedCodec}

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
				Attributes: getResponseAttributes(formats),
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

// GetCodecAttributes returns SDP attributes for codec rtpmap and fmtp
func GetCodecAttributes(formats []string) []sdp.Attribute {
	// Map of standard codec payload types to rtpmap strings
	rtpmapMap := map[string]string{
		"0":   "PCMU/8000",
		"8":   "PCMA/8000",
		"18":  "G729/8000",
		"96":  "opus/48000/2",
		"97":  "iLBC/8000",
		"98":  "speex/8000",
		"101": "telephone-event/8000",
		"99":  "G723/8000",
		"100": "G726-32/8000",
	}

	attrs := []sdp.Attribute{}

	// Add rtpmap attributes for each codec
	for _, format := range formats {
		if rtpmap, ok := rtpmapMap[format]; ok {
			attrs = append(attrs, sdp.Attribute{
				Key:   "rtpmap",
				Value: format + " " + rtpmap,
			})
		}
	}

	// Add fmtp for telephone-event
	for _, format := range formats {
		if format == "101" {
			attrs = append(attrs, sdp.Attribute{
				Key:   "fmtp",
				Value: "101 0-15",
			})
		}
	}

	// Add ptime:20 (20ms frames) - standard for VoIP
	attrs = append(attrs, sdp.Attribute{
		Key:   "ptime",
		Value: "20",
	})

	// Add sendrecv mode
	attrs = append(attrs, sdp.Attribute{
		Key: "sendrecv",
	})

	return attrs
}

// getResponseAttributes returns attributes for SDP response (includes rtcp-mux)
func getResponseAttributes(formats []string) []sdp.Attribute {
	attrs := GetCodecAttributes(formats)

	// Add rtcp-mux (RFC 5761) - means RTCP is on same port as RTP
	attrs = append(attrs, sdp.Attribute{
		Key: "rtcp-mux",
	})

	return attrs
}
