package media

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/pion/rtp"
)

const (
	frameSize     = 160 // 160 samples per 20ms frame at 8000 Hz
	frameDuration = 20 * time.Millisecond
)

// LocalService implements MediaService for in-process media handling
type LocalService struct {
	codecs      *CodecManager
	activeCalls map[string]context.CancelFunc // Track active playback by call ID
	mu          sync.RWMutex
}

// NewLocalService creates a new local media service
func NewLocalService() *LocalService {
	return &LocalService{
		codecs:      NewCodecManager(),
		activeCalls: make(map[string]context.CancelFunc),
	}
}

// Play implements MediaService.Play - streams audio to client endpoint
func (s *LocalService) Play(ctx context.Context, req PlayRequest) error {
	if req.CallID == "" || req.File == "" || req.Codec == "" || req.Port == 0 {
		return fmt.Errorf("invalid play request: missing required fields")
	}

	// Get codec configuration (req.Codec can be name or payload type string)
	codecCfg, err := s.codecs.GetByPayloadTypeString(req.Codec)
	if err != nil {
		return fmt.Errorf("unsupported codec: %s", req.Codec)
	}

	// Check if there's already active playback for this call
	s.mu.Lock()
	if _, exists := s.activeCalls[req.CallID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("playback already active for call %s", req.CallID)
	}

	// Create cancellation context for this playback
	playCtx, cancel := context.WithCancel(ctx)
	s.activeCalls[req.CallID] = cancel
	s.mu.Unlock()

	// Start playback asynchronously (returns immediately)
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.activeCalls, req.CallID)
			s.mu.Unlock()
		}()

		if err := s.streamAudio(playCtx, req, codecCfg); err != nil {
			slog.Error("[Media] Playback failed", "call_id", req.CallID, "error", err)
			if req.OnError != nil {
				req.OnError(req.CallID, err)
			}
		}
	}()

	return nil
}

// Stop implements MediaService.Stop - cancels active playback for a call
func (s *LocalService) Stop(callID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cancel, exists := s.activeCalls[callID]
	if !exists {
		return fmt.Errorf("no active playback for call %s", callID)
	}

	cancel()
	delete(s.activeCalls, callID)
	return nil
}

// Ready implements MediaService.Ready - checks if service is ready
func (s *LocalService) Ready() bool {
	return s.codecs != nil
}

// streamAudio handles the actual RTP streaming to the client
func (s *LocalService) streamAudio(ctx context.Context, req PlayRequest, codecCfg *CodecConfig) error {
	slog.Info("[Media] Starting playback",
		"call_id", req.CallID,
		"file", req.File,
		"codec", req.Codec,
		"local", fmt.Sprintf("%s:%d", req.LocalAddr, req.LocalPort),
		"remote", fmt.Sprintf("%s:%d", req.Endpoint, req.Port))

	// Read and parse WAV file
	audioFile, err := ReadWAVFile(req.File)
	if err != nil {
		return fmt.Errorf("failed to read audio file: %w", err)
	}

	// Resample to codec's format using codec's resampler function
	encodedAudio, err := codecCfg.Resampler(audioFile)
	if err != nil {
		return fmt.Errorf("failed to encode audio: %w", err)
	}

	// Bind to local RTP port (the one advertised in SDP)
	// Use 0.0.0.0 to bind to all interfaces, but use the specific port
	localAddr := &net.UDPAddr{
		Port: req.LocalPort,
		IP:   net.IPv4zero,
	}

	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to local RTP port %d: %w", req.LocalPort, err)
	}
	defer conn.Close()

	// Remote client's RTP endpoint
	clientAddr := &net.UDPAddr{
		Port: req.Port,
		IP:   net.ParseIP(req.Endpoint),
	}

	// Calculate frame parameters
	// PCMU uses 8 bits per sample (µ-law encoded), so 160 samples = 160 bytes
	bytesPerFrame := frameSize // 160 bytes for PCMU (8-bit encoded)
	rtpSeq := uint16(0)
	rtpTs := uint32(0)

	frameCount := (len(encodedAudio) + bytesPerFrame - 1) / bytesPerFrame
	framesSent := 0

	slog.Debug("[Media] Streaming setup", "frames_total", frameCount, "bytes_per_frame", bytesPerFrame)

	// Stream frames
	for i := 0; i+bytesPerFrame <= len(encodedAudio); i += bytesPerFrame {
		// Check for cancellation (BYE received or Stop() called)
		select {
		case <-ctx.Done():
			slog.Info("[Media] Playback cancelled", "call_id", req.CallID, "frames_sent", framesSent)
			return nil
		default:
		}

		frame := encodedAudio[i : i+bytesPerFrame]

		// Create RTP packet
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Padding:        false,
				Extension:      false,
				Marker:         false,
				PayloadType:    uint8(codecCfg.PayloadType),
				SequenceNumber: rtpSeq,
				Timestamp:      rtpTs,
				SSRC:           1234, // Arbitrary but consistent SSRC
			},
			Payload: frame,
		}

		// Marshal and send
		data, err := packet.Marshal()
		if err != nil {
			return fmt.Errorf("failed to marshal RTP packet: %w", err)
		}

		if _, err := conn.WriteToUDP(data, clientAddr); err != nil {
			return fmt.Errorf("failed to send RTP packet to %s:%d: %w", req.Endpoint, req.Port, err)
		}

		framesSent++
		rtpSeq++
		rtpTs += frameSize

		// Rate-limit to real-time playback speed (20ms per frame)
		time.Sleep(frameDuration)
	}

	slog.Info("[Media] Playback complete", "call_id", req.CallID, "frames_sent", framesSent, "total_frames", frameCount)

	// Call the completion callback if provided
	if req.OnComplete != nil {
		if err := req.OnComplete(req.CallID, nil); err != nil {
			slog.Error("[Media] Completion callback failed", "call_id", req.CallID, "error", err)
			return err
		}
	}

	return nil
}
