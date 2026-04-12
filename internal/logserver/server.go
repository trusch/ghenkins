package logserver

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/trusch/ghenkins/internal/auth"
	"github.com/trusch/ghenkins/internal/store"
)

//go:embed ui/index.html
var uiFS embed.FS

type contextKey string

const userContextKey contextKey = "user"

func userFromContext(ctx context.Context) *store.User {
	u, _ := ctx.Value(userContextKey).(*store.User)
	return u
}

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

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		sess, err := s.store.GetSession(r.Context(), cookie.Value)
		if err != nil || sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		user, err := s.store.GetUserByID(r.Context(), sess.UserID)
		if err != nil || user == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) adminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		user := userFromContext(r.Context())
		if user == nil || user.Role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next(w, r)
	})
}

// Handler returns the HTTP handler for the log server (useful for testing).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static UI
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, uiFS, "ui/index.html")
	})

	// Unprotected auth/setup routes
	mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// Protected routes
	mux.HandleFunc("POST /api/auth/logout", s.authMiddleware(s.handleLogout))
	mux.HandleFunc("GET /api/me", s.authMiddleware(s.handleMe))

	// Runs (protected)
	mux.HandleFunc("GET /runs", s.authMiddleware(s.handleListRuns))
	mux.HandleFunc("GET /runs/{id}", s.authMiddleware(s.handleGetRun))
	mux.HandleFunc("GET /runs/{id}/log", s.authMiddleware(s.handleRunLog))
	mux.HandleFunc("GET /runs/{id}/jobs", s.authMiddleware(s.handleListRunJobs))

	// Watches (protected)
	mux.HandleFunc("GET /api/watches", s.authMiddleware(s.handleListWatches))
	mux.HandleFunc("POST /api/watches", s.authMiddleware(s.handleCreateWatch))
	mux.HandleFunc("GET /api/watches/{name}", s.authMiddleware(s.handleGetWatch))
	mux.HandleFunc("PUT /api/watches/{name}", s.authMiddleware(s.handleUpdateWatch))
	mux.HandleFunc("DELETE /api/watches/{name}", s.authMiddleware(s.handleDeleteWatch))
	mux.HandleFunc("POST /api/watches/{name}/workflows", s.authMiddleware(s.handleCreateWorkflow))
	mux.HandleFunc("PUT /api/watches/{name}/workflows/{wfname}", s.authMiddleware(s.handleUpdateWorkflow))
	mux.HandleFunc("DELETE /api/watches/{name}/workflows/{wfname}", s.authMiddleware(s.handleDeleteWorkflow))
	mux.HandleFunc("GET /api/watches/{name}/workflows/{wfname}/content", s.authMiddleware(s.handleGetWorkflowContent))
	mux.HandleFunc("PUT /api/watches/{name}/workflows/{wfname}/content", s.authMiddleware(s.handlePutWorkflowContent))

	// Users (admin-only, except self password change)
	mux.HandleFunc("GET /api/users", s.adminMiddleware(s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.adminMiddleware(s.handleCreateAPIUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.adminMiddleware(s.handleDeleteUser))
	mux.HandleFunc("PUT /api/users/{id}/password", s.authMiddleware(s.handleUpdatePassword))

	return mux
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

// ---- Setup & Auth handlers ----

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.UserCount(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": count == 0})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.UserCount(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if count != 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "setup already completed"})
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password required"})
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	userID := generateID()
	if err := s.store.CreateUser(r.Context(), userID, body.Username, hash, "admin"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create user: " + err.Error()})
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	if err := s.store.CreateSession(r.Context(), token, userID, expiresAt); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"username": body.Username, "role": "admin"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	user, err := s.store.GetUserByUsername(r.Context(), body.Username)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	if !auth.CheckPassword(user.PasswordHash, body.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	if err := s.store.CreateSession(r.Context(), token, user.ID, expiresAt); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"username": user.Username, "role": user.Role})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:    "session",
		Value:   "",
		Path:    "/",
		MaxAge:  -1,
		Expires: time.Unix(0, 0),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{
		"id":       user.ID,
		"username": user.Username,
		"role":     user.Role,
	})
}

// ---- Runs handlers ----

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

func (s *Server) handleListRunJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	jobs, err := s.store.ListRunJobs(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if jobs == nil {
		jobs = []*store.RunJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

// ---- Watches handlers ----

func (s *Server) handleListWatches(w http.ResponseWriter, r *http.Request) {
	watches, err := s.store.ListWatches(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if watches == nil {
		watches = []*store.DBWatch{}
	}
	writeJSON(w, http.StatusOK, watches)
}

func (s *Server) handleCreateWatch(w http.ResponseWriter, r *http.Request) {
	var body watchRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if body.Name == "" || body.Repo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and repo required"})
		return
	}

	dbw := body.toDBWatch()
	if err := s.store.CreateWatch(r.Context(), dbw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, dbw)
}

func (s *Server) handleGetWatch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	watch, err := s.store.GetWatch(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, watch)
}

func (s *Server) handleUpdateWatch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body watchRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	body.Name = name
	dbw := body.toDBWatch()
	if err := s.store.UpdateWatch(r.Context(), dbw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	updated, err := s.store.GetWatch(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusOK, dbw)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteWatch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteWatch(r.Context(), name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	watchName := r.PathValue("name")
	var body workflowRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if body.Name == "" || body.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and path required"})
		return
	}
	wf := body.toDBWorkflow(watchName)
	if err := s.store.CreateWorkflow(r.Context(), wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, wf)
}

func (s *Server) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	watchName := r.PathValue("name")
	wfName := r.PathValue("wfname")
	var body workflowRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	body.Name = wfName
	wf := body.toDBWorkflow(watchName)
	if err := s.store.UpdateWorkflow(r.Context(), wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	watchName := r.PathValue("name")
	wfName := r.PathValue("wfname")
	if err := s.store.DeleteWorkflow(r.Context(), watchName, wfName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleGetWorkflowContent(w http.ResponseWriter, r *http.Request) {
	watchName := r.PathValue("name")
	wfName := r.PathValue("wfname")

	watch, err := s.store.GetWatch(r.Context(), watchName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "watch not found"})
		return
	}

	var wfPath string
	for _, wf := range watch.Workflows {
		if wf.Name == wfName {
			wfPath = wf.Path
			break
		}
	}
	if wfPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workflow not found"})
		return
	}

	data, err := os.ReadFile(wfPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "workflow file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read error: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

func (s *Server) handlePutWorkflowContent(w http.ResponseWriter, r *http.Request) {
	watchName := r.PathValue("name")
	wfName := r.PathValue("wfname")

	watch, err := s.store.GetWatch(r.Context(), watchName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "watch not found"})
		return
	}

	var wfPath string
	for _, wf := range watch.Workflows {
		if wf.Name == wfName {
			wfPath = wf.Path
			break
		}
	}
	if wfPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "workflow not found"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
		return
	}

	if err := os.MkdirAll(filepath.Dir(wfPath), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create dirs: " + err.Error()})
		return
	}
	if err := os.WriteFile(wfPath, body, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write error: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- Users handlers ----

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if users == nil {
		users = []*store.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateAPIUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password required"})
		return
	}
	if body.Role == "" {
		body.Role = "user"
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	id := generateID()
	if err := s.store.CreateUser(r.Context(), id, body.Username, hash, body.Role); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	user, _ := s.store.GetUserByID(r.Context(), id)
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleUpdatePassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	currentUser := userFromContext(r.Context())

	// Allow if admin or self
	if currentUser.Role != "admin" && currentUser.ID != id {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password required"})
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), id, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ---- Request body types ----

type watchRequestBody struct {
	Name      string                `json:"name"`
	Repo      string                `json:"repo"`
	Branch    string                `json:"branch"`
	PR        int                   `json:"pr"`
	OnEvents  []string              `json:"on_events"`
	Workflows []workflowRequestBody `json:"workflows"`
}

func (b *watchRequestBody) toDBWatch() *store.DBWatch {
	w := &store.DBWatch{
		Name:      b.Name,
		Repo:      b.Repo,
		Branch:    b.Branch,
		PR:        b.PR,
		OnEvents:  b.OnEvents,
		Workflows: []*store.DBWorkflow{},
	}
	if len(w.OnEvents) == 0 {
		w.OnEvents = []string{"push"}
	}
	for _, wb := range b.Workflows {
		w.Workflows = append(w.Workflows, wb.toDBWorkflow(b.Name))
	}
	return w
}

type workflowRequestBody struct {
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	RunnerImage string            `json:"runner_image"`
	Secrets     map[string]string `json:"secrets"`
	Env         map[string]string `json:"env"`
}

func (b *workflowRequestBody) toDBWorkflow(watchName string) *store.DBWorkflow {
	wf := &store.DBWorkflow{
		WatchName:   watchName,
		Name:        b.Name,
		Path:        b.Path,
		RunnerImage: b.RunnerImage,
		Secrets:     b.Secrets,
		Env:         b.Env,
	}
	if wf.Secrets == nil {
		wf.Secrets = map[string]string{}
	}
	if wf.Env == nil {
		wf.Env = map[string]string{}
	}
	return wf
}

// ---- Helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 3600,
	})
}

func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
