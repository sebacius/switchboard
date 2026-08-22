package llm

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// The startup banner echoes whatever flags it was given, so a supervisor
// pointed at a dead server, or at a live one missing the configured model, looks
// like a perfectly healthy boot. The failure surfaces on the first real call —
// mid-INVITE, with a caller on the line — as a turn deadline, which reads like
// "the model is broken" rather than "the model was never there".
//
// This mirrors rtpmanager/server/probe.go, for the same reason and with the same
// posture: warn, never block. The supervisor is not required for a call to be
// routed — deterministic resolution handles a registered extension with no model
// at all — so a missing LLM must not stop the SIP stack coming up.

// warmupTimeout bounds the load-the-model request. It is generous because that
// is the entire point: this call exists to absorb a multi-gigabyte model load so
// that a caller never does.
const warmupTimeout = 5 * time.Minute

// ProbeAndWarm checks the supervisor's model is reachable and present, then —
// for a provider that loads models on demand — loads it. It logs its findings
// and never returns an error, because nothing it discovers should stop the
// server: a deployment whose LLM is down still routes every call its tenant
// routing tables can resolve.
//
// What it does with what it finds is driven by the provider's ProbeProfile, so
// an operator on a hosted API is never told to run `ollama pull`, and a partial
// gateway model listing never raises a false alarm about a model that works.
//
// Run it in a goroutine — a cold model load can take minutes, and the SIP stack
// must not wait on it.
func ProbeAndWarm(ctx context.Context, p Prober, model string, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "llm-probe", "server", p.ServerURL(), "model", model)
	profile := p.ProbeProfile()

	probeTimeout := profile.ProbeTimeout
	if probeTimeout == 0 {
		probeTimeout = 5 * time.Second
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	names, err := p.ListModels(probeCtx)
	cancel()
	if err != nil {
		// Credentials rejected is a different problem from nobody answering, and
		// says so: the server is fine, the deployment just cannot use it.
		if errors.Is(err, ErrUnauthorized) {
			log.Warn("LLM server rejected our credentials; every supervised call will fail. "+
				"Check OPENAI_API_KEY. Deterministic routing is unaffected",
				"error", err)
			return
		}
		log.Warn("LLM server did not answer; supervised calls will fail until it does. "+
			"Deterministic routing is unaffected — check --llm-server",
			"error", err)
		return
	}

	present := false
	for _, n := range names {
		if n == model {
			present = true
			break
		}
	}

	if !present {
		if profile.ModelListAuthoritative {
			// The single most confusing failure this probe exists to catch: the
			// server is healthy, so every other signal looks fine, and each call
			// fails on a model the host has never heard of.
			log.Warn("LLM server is up but does NOT have the configured model; every supervised call will fail. "+
				profile.MissingModelHint,
				"available", names)
			return
		}
		// The listing is advisory for this provider, so absence proves nothing —
		// say what was seen without claiming the model is unavailable.
		log.Info("LLM server reachable; the configured model is not in its listing, "+
			"which is advisory for this provider and does not mean it is unavailable",
			"listed", len(names))
	}

	if !profile.Warmable {
		// Nothing to preload: the provider serves models it already holds, and a
		// warm-up request would buy nothing and may be billed.
		log.Info("LLM ready", "note", "hosted provider: no model load to absorb, so no warm-up was sent")
		return
	}

	warmCtx, cancelWarm := context.WithTimeout(ctx, warmupTimeout)
	defer cancelWarm()

	took, err := p.Warm(warmCtx, model)
	if err != nil {
		log.Warn("LLM model present but the warm-up request failed; the first caller will pay the model load",
			"error", err, "elapsed", took.Round(time.Millisecond))
		return
	}

	log.Info("LLM ready and warmed", "load_time", took.Round(time.Millisecond),
		"note", "this is what the first caller would otherwise have waited inside their turn budget")
}
