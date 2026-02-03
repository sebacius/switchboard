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

// playbackState tracks both cancel func and done channel for a playback
type playbackState struct {
	cancel context.CancelFunc
	done   chan struct{} // Closed when playback goroutine exits
}

// LocalService implements MediaService for in-process media handling
type LocalService struct {
	codecs      *CodecManager
	activeCalls map[string]*playbackState // Track active playback by call ID
	mu          sync.RWMutex
}

// NewLocalService creates a new local media service
func NewLocalService() *LocalService {
	return &LocalService{
		codecs:      NewCodecManager(),
		activeCalls: make(map[string]*playbackState),
	}
}

// Play implements MediaService.Play - streams audio to client endpoint
func (s *LocalService) Play(ctx context.Context, req PlayRequest) error {
	// Build file list (prefer Files over File for backwards compatibility)
	files := req.Files
	if len(files) == 0 && req.File != "" {
		files = []string{req.File}
	}

	if req.CallID == "" || len(files) == 0 || req.Codec == "" || req.Port == 0 {
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

	// Create cancellation context and done channel for this playback
	playCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.activeCalls[req.CallID] = &playbackState{
		cancel: cancel,
		done:   done,
	}
	s.mu.Unlock()

	// Start playback asynchronously (returns immediately)
	go func() {
		defer func() {
			close(done) // Signal that playback goroutine has exited
			s.mu.Lock()
			delete(s.activeCalls, req.CallID)
			s.mu.Unlock()
		}()

		if err := s.streamPlaylist(playCtx, req, files, codecCfg); err != nil {
			slog.Error("[Media] Playback failed", "call_id", req.CallID, "error", err)
			if req.OnError != nil {
				req.OnError(req.CallID, err)
			}
		}
	}()

	return nil
}

// Stop implements MediaService.Stop - cancels active playback for a call
// and waits for the playback goroutine to finish (socket to be released)
func (s *LocalService) Stop(callID string) error {
	s.mu.Lock()
	state, exists := s.activeCalls[callID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("no active playback for call %s", callID)
	}
	// Get references before unlocking
	cancel := state.cancel
	done := state.done
	s.mu.Unlock()

	// Cancel the playback context
	cancel()

	// Wait for the playback goroutine to exit (socket released)
	// Use a timeout to prevent indefinite blocking
	select {
	case <-done:
		// Playback goroutine exited, socket is released
	case <-time.After(500 * time.Millisecond):
		// Timeout - log warning but continue
		slog.Warn("[Media] Timeout waiting for playback to stop", "call_id", callID)
	}

	return nil
}

// Ready implements MediaService.Ready - checks if service is ready
func (s *LocalService) Ready() bool {
	return s.codecs != nil
}

// streamPlaylist plays a list of audio files, optionally looping.
func (s *LocalService) streamPlaylist(ctx context.Context, req PlayRequest, files []string, codecCfg *CodecConfig) error {
	slog.Info("[Media] Starting playlist playback",
		"call_id", req.CallID,
		"files", files,
		"loop", req.Loop,
		"codec", req.Codec,
		"local", fmt.Sprintf("%s:%d", req.LocalAddr, req.LocalPort),
		"remote", fmt.Sprintf("%s:%d", req.Endpoint, req.Port))

	// Bind to local RTP port once for all files
	localAddr := &net.UDPAddr{
		Port: req.LocalPort,
		IP:   net.IPv4zero,
	}

	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to bind to local RTP port %d: %w", req.LocalPort, err)
	}
	defer func() { _ = conn.Close() }()

	clientAddr := &net.UDPAddr{
		Port: req.Port,
		IP:   net.ParseIP(req.Endpoint),
	}

	// Initialize RTP state (persists across files for seamless playback)
	rtpSeq := GenerateSequenceStart()
	rtpTs := GenerateTimestampStart()
	ssrc := GenerateSSRC()

	totalFramesSent := 0

	// Play files in loop (or once if loop=false)
	for {
		for _, file := range files {
			// Check for cancellation before each file
			select {
			case <-ctx.Done():
				slog.Info("[Media] Playlist stopped",
					"call_id", req.CallID,
					"frames_sent", totalFramesSent,
				)
				return nil
			default:
			}

			framesSent, newSeq, newTs, err := s.streamSingleFile(ctx, file, conn, clientAddr, codecCfg, rtpSeq, rtpTs, ssrc)
			if err != nil {
				return err
			}

			totalFramesSent += framesSent
			rtpSeq = newSeq
			rtpTs = newTs
		}

		// If not looping, we're done
		if !req.Loop {
			break
		}

		slog.Debug("[Media] Looping playlist",
			"call_id", req.CallID,
			"frames_sent_so_far", totalFramesSent,
		)
	}

	slog.Info("[Media] Playlist complete",
		"call_id", req.CallID,
		"frames_sent", totalFramesSent,
	)

	// Call the completion callback if provided
	if req.OnComplete != nil {
		if err := req.OnComplete(req.CallID, nil); err != nil {
			slog.Error("[Media] Completion callback failed", "call_id", req.CallID, "error", err)
			return err
		}
	}

	return nil
}

// streamSingleFile streams a single audio file, returning updated RTP state.
func (s *LocalService) streamSingleFile(ctx context.Context, filePath string, conn *net.UDPConn, clientAddr *net.UDPAddr, codecCfg *CodecConfig, rtpSeq uint16, rtpTs uint32, ssrc uint32) (framesSent int, newSeq uint16, newTs uint32, err error) {
	// Read and parse WAV file
	audioFile, err := ReadWAVFile(filePath)
	if err != nil {
		return 0, rtpSeq, rtpTs, fmt.Errorf("failed to read audio file %s: %w", filePath, err)
	}

	// Resample to codec's format
	encodedAudio, err := codecCfg.Resampler(audioFile)
	if err != nil {
		return 0, rtpSeq, rtpTs, fmt.Errorf("failed to encode audio: %w", err)
	}

	bytesPerFrame := frameSize // 160 bytes for PCMU

	// Stream frames
	for i := 0; i+bytesPerFrame <= len(encodedAudio); i += bytesPerFrame {
		select {
		case <-ctx.Done():
			return framesSent, rtpSeq, rtpTs, nil
		default:
		}

		frame := encodedAudio[i : i+bytesPerFrame]

		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Padding:        false,
				Extension:      false,
				Marker:         false,
				PayloadType:    uint8(codecCfg.PayloadType),
				SequenceNumber: rtpSeq,
				Timestamp:      rtpTs,
				SSRC:           ssrc,
			},
			Payload: frame,
		}

		data, err := packet.Marshal()
		if err != nil {
			return framesSent, rtpSeq, rtpTs, fmt.Errorf("failed to marshal RTP packet: %w", err)
		}

		if _, err := conn.WriteToUDP(data, clientAddr); err != nil {
			return framesSent, rtpSeq, rtpTs, fmt.Errorf("failed to send RTP packet: %w", err)
		}

		framesSent++
		rtpSeq++
		rtpTs += frameSize

		time.Sleep(frameDuration)
	}

	return framesSent, rtpSeq, rtpTs, nil
}
