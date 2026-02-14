package media

// PlayRequest is a request to play audio to a client
type PlayRequest struct {
	CallID     string                                      // SIP Call-ID for tracking
	File       string                                      // Path to audio file (e.g., "audio/demo.wav") - deprecated, use Files
	Files      []string                                    // Playlist of audio files (preferred over File)
	RawAudio   []byte                                      // Raw WAV audio data (alternative to Files, for TTS)
	Loop       bool                                        // Loop the playlist indefinitely
	Codec      string                                      // Selected codec (PCMU, PCMA, Opus, G729)
	LocalAddr  string                                      // Local IP address to send from
	LocalPort  int                                         // Local RTP port to send from (as advertised in SDP)
	Endpoint   string                                      // Client IP address (e.g., "192.168.50.129")
	Port       int                                         // Client RTP port (e.g., 50162)
	OnComplete func(callID string, data interface{}) error // Optional callback when playback finishes
	OnError    func(callID string, err error)              // Optional callback when playback fails
}
