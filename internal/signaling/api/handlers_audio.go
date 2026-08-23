package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// What audio the flows ask for, and what the player actually has.
//
// The two halves come from different processes. The signaling server knows which
// files the graphs name; only the RTP manager has the directory. Joining them is
// the whole value: a flow naming a file nobody shipped is silent at 2am, and a
// file present in the wrong format is silent in a way that looks fine on disk.
//
// If the manager cannot answer, the references are still listed — marked
// "unknown", never "missing". Accusing a file of being absent when we merely
// could not look is the one wrong answer this endpoint could give.

// AudioProvider joins flow references with the player's files.
type AudioProvider interface {
	AudioReferences() []dialplan.AudioRef
	ListAudio(ctx context.Context) (mediaclient.AudioListing, error)
}

// SetAudioProvider wires the audio inventory.
func (s *Server) SetAudioProvider(p AudioProvider) { s.audio = p }

// audioFileView is one file, with who asks for it.
type audioFileView struct {
	Name          string              `json:"name"`
	SizeBytes     int64               `json:"size_bytes"`
	ModifiedUnix  int64               `json:"modified_unix"`
	Playable      bool                `json:"playable"`
	SampleRate    uint32              `json:"sample_rate"`
	Channels      uint32              `json:"channels"`
	BitsPerSample uint32              `json:"bits_per_sample"`
	DurationMs    int64               `json:"duration_ms"`
	Problem       string              `json:"problem"`
	ReferencedBy  []dialplan.AudioRef `json:"referenced_by"`
}

// unresolvedView is a file a flow names that is not in the listing.
type unresolvedView struct {
	File string `json:"file"`
	// State is "missing" when the manager answered and did not have it, and
	// "unknown" when the manager could not be asked at all.
	State        string              `json:"state"`
	ReferencedBy []dialplan.AudioRef `json:"referenced_by"`
}

// handleConfigAudio handles GET /api/v1/config/audio.
func (s *Server) handleConfigAudio(w http.ResponseWriter, r *http.Request) {
	if s.audio == nil {
		http.Error(w, "Audio inventory not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	refs := s.audio.AudioReferences()
	byFile := map[string][]dialplan.AudioRef{}
	for _, ref := range refs {
		byFile[ref.File] = append(byFile[ref.File], ref)
	}

	listing, listErr := s.audio.ListAudio(r.Context())

	out := map[string]any{
		"base_path":   listing.BasePath,
		"working_dir": listing.WorkingDir,
		"truncated":   listing.Truncated,
	}

	if listErr != nil {
		// Every reference becomes "unknown". The UI must not report a file as
		// missing on the strength of a failed lookup.
		out["source"] = "unavailable"
		out["error"] = listErr.Error()
		out["files"] = []audioFileView{}
		out["unresolved"] = unresolved(byFile, nil, "unknown")
		out["unreferenced"] = []string{}
		s.writeJSON(w, out)
		return
	}

	source := listing.Source
	if source == "" {
		source = "rtpmanager"
	}
	out["source"] = source

	present := map[string]bool{}
	files := make([]audioFileView, 0, len(listing.Files))
	var unreferenced []string
	for _, f := range listing.Files {
		present[f.Name] = true
		view := audioFileView{
			Name:          f.Name,
			SizeBytes:     f.SizeBytes,
			ModifiedUnix:  f.ModifiedUnix,
			Playable:      f.Playable,
			SampleRate:    f.SampleRate,
			Channels:      f.Channels,
			BitsPerSample: f.BitsPerSample,
			DurationMs:    f.DurationMs,
			Problem:       f.Problem,
			ReferencedBy:  byFile[f.Name],
		}
		if view.ReferencedBy == nil {
			view.ReferencedBy = []dialplan.AudioRef{}
			unreferenced = append(unreferenced, f.Name)
		}
		files = append(files, view)
	}
	sort.Strings(unreferenced)
	if unreferenced == nil {
		unreferenced = []string{}
	}

	out["files"] = files
	out["unresolved"] = unresolved(byFile, present, "missing")
	out["unreferenced"] = unreferenced

	// A flow's file name is opened relative to the RTP manager's working
	// directory only if the two agree. When they do not, a file can be listed
	// here and still fail to play, which is the exact confusion worth naming.
	if listing.BasePath != "" && listing.WorkingDir != "" && listing.BasePath != listing.WorkingDir {
		out["resolution_note"] = "the audio directory and the RTP manager's working directory differ; " +
			"a play_audio name is resolved against the audio directory"
	}

	s.writeJSON(w, out)
}

// unresolved lists referenced files not present in the listing.
func unresolved(byFile map[string][]dialplan.AudioRef, present map[string]bool, state string) []unresolvedView {
	out := []unresolvedView{}
	names := make([]string, 0, len(byFile))
	for name := range byFile {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if present[name] {
			continue
		}
		out = append(out, unresolvedView{File: name, State: state, ReferencedBy: byFile[name]})
	}
	return out
}
