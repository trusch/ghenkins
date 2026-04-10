package logserver

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/trusch/ghenkins/internal/store"
)

//go:embed ui/index.html
var uiFS embed.FS

type Server struct {
	bind     string
	logDir   string
	store    store.Store
	maxBytes int64
	maxAge   time.Duration
	log      zerolog.Logger
}

func New(bind, logDir string, st store.Store, maxBytes int64, maxAge time.Duration, log zerolog.Logger) *Server {
	return &Server{
		bind:     bind,
		logDir:   logDir,
		store:    st,
		maxBytes: maxBytes,
		maxAge:   maxAge,
		log:      log,
	}
}

// LogPath returns the absolute path for a run's log file.
func (s *Server) LogPath(runID string) string {
	return filepath.Join(s.logDir, runID+".log")
}

// Handler returns the HTTP handler for the log server (useful for testing).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, uiFS, "ui/index.html")
	})
	mux.HandleFunc("GET /runs", s.handleListRuns)
	mux.HandleFunc("GET /runs/{id}", s.handleGetRun)
	mux.HandleFunc("GET /runs/{id}/log", s.handleRunLog)
	return mux
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(run) //nolint:errcheck
}

// Run starts the HTTP server and retention goroutine. Blocks until ctx done.
func (s *Server) Run(ctx context.Context) error {
	if err := os.MkdirAll(s.logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	srv := &http.Server{
		Addr:    s.bind,
		Handler: s.Handler(),
	}

	go s.runRetention(ctx)

	errCh := make(chan error, 1)
	go func() {
		s.log.Info().Str("bind", s.bind).Msg("log server started")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListRuns(r.Context(), 50)
	if err != nil {
		s.log.Error().Err(err).Msg("list runs")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []store.Run{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs) //nolint:errcheck
}

func isTerminal(status store.RunStatus) bool {
	return status == store.RunStatusSuccess ||
		status == store.RunStatusFailure ||
		status == store.RunStatusError ||
		status == store.RunStatusCanceled
}

func (s *Server) handleRunLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if isTerminal(run.Status) {
		f, err := os.Open(s.LogPath(id))
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			http.Error(w, "cannot open log", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		io.Copy(w, f) //nolint:errcheck
		return
	}

	// Running: stream with 500ms poll loop and chunked transfer.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		// Open file once it appears.
		if f == nil {
			f, _ = os.Open(s.LogPath(id))
		}

		// Drain any available bytes.
		if f != nil {
			for {
				n, _ := f.Read(buf)
				if n == 0 {
					break
				}
				w.Write(buf[:n]) //nolint:errcheck
			}
			flusher.Flush()
		}

		// Check if run has reached a terminal state.
		cur, err := s.store.GetRun(r.Context(), id)
		if err != nil || isTerminal(cur.Status) {
			// Final drain.
			if f != nil {
				for {
					n, _ := f.Read(buf)
					if n == 0 {
						break
					}
					w.Write(buf[:n]) //nolint:errcheck
				}
				flusher.Flush()
			}
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
