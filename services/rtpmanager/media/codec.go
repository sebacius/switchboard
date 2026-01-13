package media

import (
	"fmt"
	"log/slog"
	"time"
)

// Codec represents an immutable audio codec specification.
// Use the pre-defined codec values (CodecPCMU, CodecPCMA, etc.) for RTP streaming.
type Codec struct {
	Name        string        // Codec name (e.g., "PCMU", "PCMA")
	PayloadType uint8         // RTP payload type (0 for PCMU, 8 for PCMA)
	SampleRate  uint32        // Sample rate in Hz (8000, 16000, 48000, etc.)
	SampleDur   time.Duration // Duration per sample frame (typically 20ms)
	Channels    int           // Number of channels (1 for mono, 2 for stereo)
}

// Pre-defined codecs for common VoIP use cases.
var (
	// CodecPCMU is G.711 µ-law (North America, Japan)
	CodecPCMU = Codec{"PCMU", 0, 8000, 20 * time.Millisecond, 1}

	// CodecPCMA is G.711 A-law (Europe, rest of world)
	CodecPCMA = Codec{"PCMA", 8, 8000, 20 * time.Millisecond, 1}

	// CodecTelephoneEvent is RFC 4733 DTMF events
	CodecTelephoneEvent = Codec{"telephone-event", 101, 8000, 20 * time.Millisecond, 1}
)

// SamplesPerFrame returns the number of samples in one frame.
// For 8kHz with 20ms frames, this returns 160.
func (c Codec) SamplesPerFrame() int {
	return int(c.SampleRate) * int(c.SampleDur) / int(time.Second)
}

// BytesPerFrame returns the payload bytes per frame.
// For PCMU/PCMA (8-bit encoded), this equals SamplesPerFrame.
// For 16-bit PCM, this would be 2 * SamplesPerFrame * Channels.
func (c Codec) BytesPerFrame() int {
	// For G.711 codecs (PCMU/PCMA), 1 byte per sample
	// This can be extended for other codecs as needed
	return c.SamplesPerFrame() * c.Channels
}

// TimestampIncrement returns the RTP timestamp increment per frame.
// This equals SamplesPerFrame for audio codecs.
func (c Codec) TimestampIncrement() uint32 {
	return uint32(c.SamplesPerFrame())
}

// CodecConfig defines properties and handling for a codec
type CodecConfig struct {
	Name        string                           // Human-readable name (e.g., "PCMU", "Opus")
	PayloadType int                              // RTP payload type (0 for PCMU, 96 for Opus, etc.)
	SampleRate  int                              // Sample rate in Hz (8000, 16000, 48000, etc.)
	Resampler   func(*AudioFile) ([]byte, error) // Function to convert audio to codec's format
}

// CodecManager manages codec configurations
type CodecManager struct {
	codecs map[string]*CodecConfig
}

// NewCodecManager creates a codec manager with default configurations
// Currently only PCMU is supported
func NewCodecManager() *CodecManager {
	cm := &CodecManager{
		codecs: make(map[string]*CodecConfig),
	}

	cm.Register("PCMU", &CodecConfig{
		Name:        "PCMU",
		PayloadType: 0,
		SampleRate:  8000,
		Resampler:   resamplePCMU,
	})

	return cm
}

// Register adds or updates a codec configuration
func (cm *CodecManager) Register(codecName string, cfg *CodecConfig) {
	cm.codecs[codecName] = cfg
	slog.Debug("[CodecMgr] Registered codec", "name", codecName, "pt", cfg.PayloadType, "sr", cfg.SampleRate)
}

// Get retrieves a codec configuration by name
func (cm *CodecManager) Get(codecName string) (*CodecConfig, error) {
	cfg, exists := cm.codecs[codecName]
	if !exists {
		return nil, fmt.Errorf("codec not supported: %s", codecName)
	}
	return cfg, nil
}

// GetByPayloadTypeString retrieves a codec by payload type string (e.g., "0", "8", "96")
func (cm *CodecManager) GetByPayloadTypeString(ptStr string) (*CodecConfig, error) {
	// Try lookup by name first (for backward compatibility)
	if cfg, err := cm.Get(ptStr); err == nil {
		return cfg, nil
	}

	// Try to find by payload type
	for _, cfg := range cm.codecs {
		if fmt.Sprintf("%d", cfg.PayloadType) == ptStr {
			return cfg, nil
		}
	}
	return nil, fmt.Errorf("codec not found for payload type: %s", ptStr)
}

// GetByPayloadType retrieves a codec configuration by RTP payload type
func (cm *CodecManager) GetByPayloadType(pt int) (*CodecConfig, error) {
	for _, cfg := range cm.codecs {
		if cfg.PayloadType == pt {
			return cfg, nil
		}
	}
	return nil, fmt.Errorf("codec not found for payload type: %d", pt)
}

// resamplePCMU resamples audio to PCMU format (8000 Hz, mono, 16-bit PCM → µ-law)
func resamplePCMU(audioFile *AudioFile) ([]byte, error) {
	// Resample to 8000 Hz mono 16-bit
	pcmData, err := ResampleAudio(audioFile)
	if err != nil {
		return nil, fmt.Errorf("failed to resample to PCMU: %w", err)
	}

	// Convert to PCMU (µ-law)
	return PCMToPCMU(pcmData), nil
}
