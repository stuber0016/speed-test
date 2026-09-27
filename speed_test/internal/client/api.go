package client

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"speedtest/internal/auth"
	"speedtest/internal/protocol"
)

//go:embed web/index.html
var webFS embed.FS

// API is the Client service's HTTP interface for controllers (CLI, web page).
type API struct {
	ctx    context.Context
	runner *Runner
	store  *Store
}

// NewAPI returns the Client HTTP handler. Tests run in the background under
// ctx, so cancelling it (on shutdown) aborts any running test.
func NewAPI(ctx context.Context, runner *Runner, store *Store, apiKey string) http.Handler {
	a := &API{ctx: ctx, runner: runner, store: store}

	api := http.NewServeMux()
	api.HandleFunc("POST /api/tests", a.startTest)
	api.HandleFunc("GET /api/tests", a.listTests)
	api.HandleFunc("GET /api/tests/{id}", a.getTest)

	mux := http.NewServeMux()
	mux.Handle("/api/", auth.Bearer(apiKey, api))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// The page itself is public; it asks for the API key and sends it on
	// every API call.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, webFS, "web/index.html")
	})
	return mux
}

func (a *API) startTest(w http.ResponseWriter, r *http.Request) {
	var cfg Config
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&cfg)
	if err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, protocol.Error{Error: "invalid JSON body: " + err.Error()})
		return
	}
	if err := cfg.Normalize(); err != nil {
		writeJSON(w, http.StatusBadRequest, protocol.Error{Error: err.Error()})
		return
	}

	t, err := a.store.Start(a.runner.TargetURL(), cfg)
	if errors.Is(err, ErrBusy) {
		writeJSON(w, http.StatusConflict, protocol.Error{Error: err.Error()})
		return
	}

	go a.run(t.ID, cfg)

	w.Header().Set("Location", "/api/tests/"+t.ID)
	writeJSON(w, http.StatusAccepted, t)
}

func (a *API) run(id string, cfg Config) {
	// Generous upper bound: two throughput phases plus pings and slack.
	ctx, cancel := context.WithTimeout(a.ctx, 2*cfg.duration()+time.Minute)
	defer cancel()

	update := func(fn func(*Test)) { a.store.Update(id, fn) }
	err := a.runner.Run(ctx, cfg, Hooks{
		Phase:    func(p Phase) { update(func(t *Test) { t.Phase, t.Progress = p, nil }) },
		Progress: func(p Progress) { update(func(t *Test) { t.Progress = &p }) },
		Latency:  func(l LatencyResult) { update(func(t *Test) { t.Latency = &l }) },
		Throughput: func(p Phase, res ThroughputResult) {
			update(func(t *Test) {
				if p == PhaseDownload {
					t.Download = &res
				} else {
					t.Upload = &res
				}
			})
		},
	})
	if err != nil {
		log.Printf("test %s failed: %v", id, err)
	} else {
		log.Printf("test %s done", id)
	}
	a.store.Finish(id, err)
}

func (a *API) listTests(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.store.List())
}

func (a *API) getTest(w http.ResponseWriter, r *http.Request) {
	t, ok := a.store.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, protocol.Error{Error: "test not found"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}
