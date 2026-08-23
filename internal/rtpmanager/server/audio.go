package server

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebas/switchboard/internal/rtpmanager/media"
	rtpv1 "github.com/sebas/switchboard/pkg/rtpmanager/v1"
)

// maxAudioListing bounds one listing. A prompt library larger than this is not
// something an operator browses in a table, and an unbounded walk over a
// mistakenly-pointed directory should not be a way to occupy this process.
const maxAudioListing = 500

// maxAudioDepth bounds how deep the walk goes. Prompts live in the audio
// directory or one folder under it; deeper is a wrong --audio-path.
const maxAudioDepth = 2

// ListAudio implements RTPManagerService.ListAudio.
//
// The signaling server has no audio directory of its own — a flow names a file
// and this service opens it — so whether that file exists, and whether it will
// play, can only be answered here.
func (s *Server) ListAudio(ctx context.Context, req *rtpv1.ListAudioRequest) (*rtpv1.ListAudioResponse, error) {
	base := s.config.AudioBasePath
	wd, _ := os.Getwd()

	resp := &rtpv1.ListAudioResponse{BasePath: base, WorkingDir: wd}
	if base == "" {
		return resp, nil
	}

	root := filepath.Clean(base)
	var files []*rtpv1.AudioFileInfo

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable subdirectory should not lose the whole listing.
			return nil
		}
		if len(files) >= maxAudioListing {
			resp.Truncated = true
			return filepath.SkipAll
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if d.IsDir() {
			if depth(rel) >= maxAudioDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".wav") {
			return nil
		}

		info := &rtpv1.AudioFileInfo{Name: filepath.ToSlash(rel)}
		if st, statErr := d.Info(); statErr == nil {
			info.SizeBytes = st.Size()
			info.ModifiedUnix = st.ModTime().Unix()
		}

		wav, probeErr := media.ProbeWAVFile(path)
		if probeErr != nil {
			info.Problem = probeErr.Error()
			info.Playable = false
		} else {
			info.SampleRate = uint32(wav.SampleRate)
			info.Channels = uint32(wav.NumChannels)
			info.BitsPerSample = uint32(wav.BitsPerSample)
			info.DurationMs = wav.DurationMs()
			info.Problem, info.Playable = wav.Problem()
		}

		files = append(files, info)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			// A configured directory that does not exist is a real answer: no
			// files, and every flow reference will fail to resolve.
			return resp, nil
		}
		slog.Warn("[gRPC] ListAudio walk failed", "path", root, "error", err)
		return resp, nil
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	resp.Files = files
	return resp, nil
}

// depth counts path segments in a relative path.
func depth(rel string) int {
	if rel == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

// resolveAudio turns a flow's file name into a path under the audio directory.
//
// A relative name is joined onto the audio directory and checked to still be
// inside it, so "../../etc/passwd" in a flow cannot read outside the prompt
// library.
//
// An absolute path is honoured as written. Deployments exist that mount prompts
// somewhere specific and say so in the flow, and today — with the audio
// directory unread — an absolute path is the ONLY thing that reliably works, so
// reinterpreting it here would break exactly the people who did the careful
// thing. Note what this means: a flow file can name any absolute path, so it is
// operator-trust input. The WAV loader rejects anything that is not RIFF/PCM, so
// the reach is "play a valid WAV that is already on this host".
//
// With no audio directory configured, names pass through unchanged, which is
// what every deployment gets today.
func (s *Server) resolveAudio(name string) string {
	base := s.config.AudioBasePath
	if name == "" || base == "" || filepath.IsAbs(name) {
		return name
	}

	root := filepath.Clean(base)
	joined := filepath.Join(root, filepath.Clean("/"+name))
	if !strings.HasPrefix(joined, root+string(filepath.Separator)) && joined != root {
		slog.Warn("[AUDIO] Refusing a file name that escapes the audio directory",
			"name", name, "base", root)
		return name
	}
	return joined
}

// resolveAudioAll resolves a playlist.
func (s *Server) resolveAudioAll(names []string) []string {
	if len(names) == 0 {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = s.resolveAudio(n)
	}
	return out
}
