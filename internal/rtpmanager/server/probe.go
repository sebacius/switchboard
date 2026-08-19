package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// The startup banner echoes whatever flags it was given, so a misconfigured
// TTS or ASR endpoint looks like a perfectly healthy boot and only fails on the
// first real call — mid-conversation, with a caller on the line. These probes
// move that discovery to startup, where it is cheap to act on.
//
// A failed probe is a WARNING, not a fatal error: the media services are
// optional (a deployment doing pure call routing needs neither), and a service
// that is merely slow to start should not stop the RTP manager from coming up.

// probeTimeout bounds a single readiness probe. It is short because this runs on
// the startup path — the point is a fast signal, not a retry loop.
const probeTimeout = 3 * time.Second

// probeMediaServices checks that the configured TTS and ASR endpoints actually
// answer the OpenAI-compatible model listing they are expected to serve. It logs
// the outcome and never blocks startup.
func probeMediaServices(ctx context.Context, cfg *Config) {
	if cfg.TTSServerURL != "" {
		probeEndpoint(ctx, "TTS", cfg.TTSServerURL, "/v1/models",
			"text-to-speech will fail on every call; check --tts-server")
	}
	if cfg.ASRServerURL != "" {
		probeEndpoint(ctx, "ASR", cfg.ASRServerURL, "/v1/models",
			"transcription will fail on every call; check --asr-server")
	}
}

// probeEndpoint issues one GET and reports whether the service looks alive. A
// non-200 is called out specifically: it usually means the URL points at some
// other HTTP service entirely (a swapped port), which is otherwise very hard to
// tell apart from a working configuration.
func probeEndpoint(ctx context.Context, name, baseURL, path, consequence string) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	url := baseURL + path
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn(fmt.Sprintf("[%s] readiness probe could not be built", name),
			"url", url, "error", err, "consequence", consequence)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn(fmt.Sprintf("[%s] server unreachable", name),
			"url", url, "error", err, "consequence", consequence)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn(fmt.Sprintf("[%s] server answered but not with a model listing", name),
			"url", url, "status", resp.StatusCode,
			"hint", "this usually means the URL points at a different service (wrong port)",
			"consequence", consequence)
		return
	}

	slog.Info(fmt.Sprintf("[%s] server reachable", name), "url", baseURL)
}
