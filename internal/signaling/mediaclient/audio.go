package mediaclient

import (
	"context"
	"fmt"

	rtpv1 "github.com/sebas/switchboard/pkg/rtpmanager/v1"
)

// Asking an RTP manager what audio it can play.
//
// This is a NARROW interface rather than a method on Transport. Transport is
// implemented by everything that carries media, and widening it would force
// every implementation to answer a question only a gRPC peer can — while this
// one is satisfied by the two types that can actually answer it.

// AudioFile is one file an RTP manager holds.
type AudioFile struct {
	Name          string `json:"name"`
	SizeBytes     int64  `json:"size_bytes"`
	ModifiedUnix  int64  `json:"modified_unix"`
	Playable      bool   `json:"playable"`
	SampleRate    uint32 `json:"sample_rate"`
	Channels      uint32 `json:"channels"`
	BitsPerSample uint32 `json:"bits_per_sample"`
	DurationMs    int64  `json:"duration_ms"`
	// Problem says why the file will not play, or why it will not sound as
	// recorded. Empty means it is exactly what the player wants.
	Problem string `json:"problem"`
}

// AudioListing is one manager's answer.
type AudioListing struct {
	// Source names which manager answered, so an operator with several knows
	// whose disk they are looking at.
	Source     string      `json:"source"`
	BasePath   string      `json:"base_path"`
	WorkingDir string      `json:"working_dir"`
	Files      []AudioFile `json:"files"`
	Truncated  bool        `json:"truncated"`
}

// AudioLister is implemented by a transport that can describe its audio files.
type AudioLister interface {
	ListAudio(ctx context.Context) (AudioListing, error)
}

// ListAudio asks this manager what it can play.
func (t *GRPCTransport) ListAudio(ctx context.Context) (AudioListing, error) {
	resp, err := t.client.ListAudio(ctx, &rtpv1.ListAudioRequest{})
	if err != nil {
		return AudioListing{}, err
	}
	return listingFrom(resp, ""), nil
}

// ListAudio asks one healthy member of the pool.
//
// One member, not all: they are expected to share an audio directory, and an
// operator comparing three identical listings learns nothing. The answer names
// which node gave it, so a discrepancy is at least visible.
func (p *Pool) ListAudio(ctx context.Context) (AudioListing, error) {
	member, err := p.selectMember()
	if err != nil {
		return AudioListing{}, err
	}
	resp, err := member.transport.client.ListAudio(ctx, &rtpv1.ListAudioRequest{})
	if err != nil {
		return AudioListing{}, fmt.Errorf("rtpmanager %s: %w", member.id, err)
	}
	return listingFrom(resp, member.id), nil
}

// listingFrom converts the wire response.
func listingFrom(resp *rtpv1.ListAudioResponse, source string) AudioListing {
	out := AudioListing{
		Source:     source,
		BasePath:   resp.BasePath,
		WorkingDir: resp.WorkingDir,
		Truncated:  resp.Truncated,
		Files:      make([]AudioFile, 0, len(resp.Files)),
	}
	for _, f := range resp.Files {
		out.Files = append(out.Files, AudioFile{
			Name:          f.Name,
			SizeBytes:     f.SizeBytes,
			ModifiedUnix:  f.ModifiedUnix,
			Playable:      f.Playable,
			SampleRate:    f.SampleRate,
			Channels:      f.Channels,
			BitsPerSample: f.BitsPerSample,
			DurationMs:    f.DurationMs,
			Problem:       f.Problem,
		})
	}
	return out
}

var (
	_ AudioLister = (*GRPCTransport)(nil)
	_ AudioLister = (*Pool)(nil)
)
