package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	types "github.com/sebas/switchboard/api/types/v1"
	"github.com/sebas/switchboard/internal/ui/config"
)

// countingBackend answers every signaling read and records what was asked for.
//
// The claim being tested is about work NOT done, which nothing else can observe:
// a page renders identically whether it fetched what it needed or fetched
// everything and threw six sevenths away.
type countingBackend struct {
	mu    sync.Mutex
	calls map[string]int
}

func newCountingBackend() *countingBackend {
	return &countingBackend{calls: map[string]int{}}
}

func (b *countingBackend) count(path string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[path]
}

func (b *countingBackend) paths() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.calls))
	for p := range b.calls {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (b *countingBackend) handler() http.Handler {
	mux := http.NewServeMux()
	record := func(path string, body any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b.mu.Lock()
			b.calls[path]++
			b.mu.Unlock()
			_ = json.NewEncoder(w).Encode(body)
		}
	}
	mux.HandleFunc("/api/v1/health", record("health", types.HealthResponse{Status: "ok"}))
	mux.HandleFunc("/api/v1/stats", record("stats", types.StatsResponse{}))
	mux.HandleFunc("/api/v1/registrations", record("registrations", []types.Registration{}))
	mux.HandleFunc("/api/v1/dialogs", record("dialogs", []types.Dialog{}))
	mux.HandleFunc("/api/v1/sessions", record("sessions", []types.Session{}))
	mux.HandleFunc("/api/v1/rtpmanagers", record("rtpmanagers", types.RtpManagersResponse{}))
	mux.HandleFunc("/api/v1/park", record("park", []types.ParkedCall{}))
	return mux
}

func newCountingUI(t *testing.T, b *countingBackend) *testUI {
	t.Helper()
	api := httptest.NewServer(b.handler())
	t.Cleanup(api.Close)

	s, err := NewServer(&config.Config{
		Port:     0,
		BindAddr: "127.0.0.1",
		Backends: []config.Backend{{Name: "test", Address: api.URL}},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return &testUI{Server: s, handler: s.httpServer.Handler}
}

// Every partial poll used to fetch all seven resources sequentially, per
// backend, and discard the six it did not render. A dialogs refresh runs every
// three seconds, so that was six wasted round trips per tick per backend.
func TestAPollFetchesOnlyWhatItRenders(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/admin/partials/dialogs", "dialogs"},
		{"/admin/partials/sessions", "sessions"},
		{"/admin/partials/registrations", "registrations"},
		{"/admin/partials/parkedcalls", "park"},
		{"/admin/partials/rtpmanagers", "rtpmanagers"},
		{"/admin/partials/stats", "stats"},
	}
	for _, c := range cases {
		t.Run(strings.TrimPrefix(c.path, "/admin/partials/"), func(t *testing.T) {
			b := newCountingBackend()
			ui := newCountingUI(t, b)

			if rec := ui.get(t, c.path); rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}

			// Health always: it gates the rest, and the backend list is built
			// from it. Plus the one resource this partial shows. Nothing else.
			wantPaths := []string{"health", c.want}
			sort.Strings(wantPaths)
			if got := b.paths(); !equalStrings(got, wantPaths) {
				t.Errorf("fetched %v, want %v", got, wantPaths)
			}
			if n := b.count(c.want); n != 1 {
				t.Errorf("%s fetched %d times, want once", c.want, n)
			}
		})
	}
}

// The backends list is health and nothing else.
func TestTheBackendsPartialFetchesOnlyHealth(t *testing.T) {
	b := newCountingBackend()
	ui := newCountingUI(t, b)
	ui.get(t, "/admin/partials/backends")

	if got := b.paths(); !equalStrings(got, []string{"health"}) {
		t.Errorf("fetched %v, want only health", got)
	}
}

// A live PAGE and its poll must ask for the same thing, or the page's first
// paint and its first refresh disagree about what they are showing.
func TestALivePageAsksForWhatItsPollAsksFor(t *testing.T) {
	for _, c := range []struct{ page, partial string }{
		{"/admin/live/dialogs", "/admin/partials/dialogs"},
		{"/admin/live/sessions", "/admin/partials/sessions"},
		{"/admin/live/registrations", "/admin/partials/registrations"},
		{"/admin/live/parked", "/admin/partials/parkedcalls"},
		{"/admin/live/rtpmanagers", "/admin/partials/rtpmanagers"},
	} {
		pb, fb := newCountingBackend(), newCountingBackend()
		newCountingUI(t, pb).get(t, c.page)
		newCountingUI(t, fb).get(t, c.partial)

		if got, want := pb.paths(), fb.paths(); !equalStrings(got, want) {
			t.Errorf("%s fetched %v but %s fetched %v", c.page, got, c.partial, want)
		}
	}
}

// An unreachable backend must not blank the rows the others returned, and must
// not cost a fetch of everything else either.
func TestAnUnhealthyBackendStopsAtHealth(t *testing.T) {
	b := newCountingBackend()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.calls["health"]++
		b.mu.Unlock()
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(api.Close)

	s, err := NewServer(&config.Config{
		Port: 0, BindAddr: "127.0.0.1",
		Backends: []config.Backend{{Name: "test", Address: api.URL}},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ui := &testUI{Server: s, handler: s.httpServer.Handler}

	if rec := ui.get(t, "/admin/live/dialogs"); rec.Code != http.StatusOK {
		t.Errorf("status = %d — a down backend must still render the page", rec.Code)
	}
	if got := b.paths(); !equalStrings(got, []string{"health"}) {
		t.Errorf("fetched %v after health failed, want nothing further", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
