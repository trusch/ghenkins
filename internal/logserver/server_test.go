package logserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trusch/ghenkins/internal/auth"
	"github.com/trusch/ghenkins/internal/logserver"
	"github.com/trusch/ghenkins/internal/store"
)

func newTestServer(t *testing.T) (*logserver.Server, store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	dbPath := filepath.Join(dir, "test.db")

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	srv := logserver.New("127.0.0.1:0", logDir, st, 100*1024*1024, 7*24*time.Hour, zerolog.Nop())
	return srv, st, logDir
}

// authedClient returns an http.Client that carries a valid admin session cookie for the given test server URL.
func authedClient(t *testing.T, st store.Store, baseURL string) *http.Client {
	t.Helper()
	ctx := context.Background()

	hash, err := auth.HashPassword("testpass")
	require.NoError(t, err)
	require.NoError(t, st.CreateUser(ctx, "test-user-id", "testadmin", hash, "admin"))

	token, err := auth.GenerateToken()
	require.NoError(t, err)
	require.NoError(t, st.CreateSession(ctx, token, "test-user-id", time.Now().Add(time.Hour)))

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	u, _ := url.Parse(baseURL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: token}})
	return &http.Client{Jar: jar}
}

func TestListRunsEmpty(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := authedClient(t, st, ts.URL)
	resp, err := client.Get(ts.URL + "/runs")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var runs []store.Run
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&runs))
	assert.Empty(t, runs)
}

func TestListRuns(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	run := &store.Run{
		ID:           "run-1",
		WatchName:    "watch",
		Repo:         "owner/repo",
		SHA:          "abc123",
		WorkflowName: "ci.yml",
		Status:       store.RunStatusSuccess,
		StartedAt:    now,
		LogPath:      "/tmp/run-1.log",
	}
	require.NoError(t, st.CreateRun(ctx, run))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := authedClient(t, st, ts.URL)
	resp, err := client.Get(ts.URL + "/runs")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var runs []store.Run
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&runs))
	require.Len(t, runs, 1)
	assert.Equal(t, "run-1", runs[0].ID)
}

func TestRunLogNotFound(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := authedClient(t, st, ts.URL)
	resp, err := client.Get(ts.URL + "/runs/nonexistent/log")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestRunLogTerminal(t *testing.T) {
	srv, st, logDir := newTestServer(t)
	ctx := context.Background()

	require.NoError(t, os.MkdirAll(logDir, 0o755))

	now := time.Now().UTC()
	fin := now.Add(time.Second)
	exitCode := 0
	run := &store.Run{
		ID:           "finished-run",
		WatchName:    "watch",
		Repo:         "owner/repo",
		SHA:          "def456",
		WorkflowName: "ci.yml",
		Status:       store.RunStatusSuccess,
		StartedAt:    now,
		FinishedAt:   &fin,
		ExitCode:     &exitCode,
		LogPath:      srv.LogPath("finished-run"),
	}
	require.NoError(t, st.CreateRun(ctx, run))

	logContent := "step 1: ok\nstep 2: ok\n"
	require.NoError(t, os.WriteFile(srv.LogPath("finished-run"), []byte(logContent), 0o644))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := authedClient(t, st, ts.URL)
	resp, err := client.Get(ts.URL + "/runs/finished-run/log")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/plain")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, logContent, string(body))
}

func TestRunLogTerminalMissingFile(t *testing.T) {
	srv, st, logDir := newTestServer(t)
	ctx := context.Background()

	require.NoError(t, os.MkdirAll(logDir, 0o755))

	now := time.Now().UTC()
	fin := now.Add(time.Second)
	exitCode := 0
	run := &store.Run{
		ID:           "no-log-run",
		WatchName:    "watch",
		Repo:         "owner/repo",
		SHA:          "ghi789",
		WorkflowName: "ci.yml",
		Status:       store.RunStatusFailure,
		StartedAt:    now,
		FinishedAt:   &fin,
		ExitCode:     &exitCode,
		LogPath:      srv.LogPath("no-log-run"),
	}
	require.NoError(t, st.CreateRun(ctx, run))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := authedClient(t, st, ts.URL)
	resp, err := client.Get(ts.URL + "/runs/no-log-run/log")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestLogPath(t *testing.T) {
	srv, _, logDir := newTestServer(t)
	assert.Equal(t, filepath.Join(logDir, "my-run.log"), srv.LogPath("my-run"))
}

func TestRetentionByAge(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	dbPath := filepath.Join(dir, "test.db")
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer st.Close()

	srv := logserver.New("127.0.0.1:0", logDir, st, 100*1024*1024, time.Hour, zerolog.Nop())

	oldFile := filepath.Join(logDir, "old.log")
	require.NoError(t, os.WriteFile(oldFile, []byte("old"), 0o644))
	pastTime := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(oldFile, pastTime, pastTime))

	newFile := filepath.Join(logDir, "new.log")
	require.NoError(t, os.WriteFile(newFile, []byte("new"), 0o644))

	srv.ApplyRetention()

	_, err = os.Stat(oldFile)
	assert.True(t, os.IsNotExist(err), "old file should be deleted")

	_, err = os.Stat(newFile)
	assert.NoError(t, err, "new file should remain")
}

func TestRetentionBySize(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	dbPath := filepath.Join(dir, "test.db")
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer st.Close()

	// maxBytes = 10 bytes, maxAge = 1 year (so age won't trigger)
	srv := logserver.New("127.0.0.1:0", logDir, st, 10, 365*24*time.Hour, zerolog.Nop())

	// Write two files: older one should be deleted first.
	older := filepath.Join(logDir, "older.log")
	require.NoError(t, os.WriteFile(older, []byte("12345678"), 0o644)) // 8 bytes
	olderTime := time.Now().Add(-10 * time.Minute)
	require.NoError(t, os.Chtimes(older, olderTime, olderTime))

	newer := filepath.Join(logDir, "newer.log")
	require.NoError(t, os.WriteFile(newer, []byte("12345678"), 0o644)) // 8 bytes — total 16 > 10

	srv.ApplyRetention()

	// older should be gone, newer should survive
	_, err = os.Stat(older)
	assert.True(t, os.IsNotExist(err), "older file should be deleted to fit size limit")

	_, err = os.Stat(newer)
	assert.NoError(t, err, "newer file should remain")
}
