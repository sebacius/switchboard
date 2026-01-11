package media

// PlayRequest is a request to play audio to a client
type PlayRequest struct {
	CallID     string                                      // SIP Call-ID for tracking
	File       string                                      // Path to audio file (e.g., "audio/demo.wav")
	Codec      string                                      // Selected codec (PCMU, PCMA, Opus, G729)
	Endpoint   string                                      // Client IP address (e.g., "192.168.50.129")
	Port       int                                         // Client RTP port (e.g., 50162)
	OnComplete func(callID string, data interface{}) error // Optional callback when playback finishes
}
