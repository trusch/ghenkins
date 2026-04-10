package poller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v67/github"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trusch/ghenkins/internal/config"
	"github.com/trusch/ghenkins/internal/poller"
	"github.com/trusch/ghenkins/internal/store"
)

// memStore implements store.Store with an in-memory map for testing.
type memStore struct {
	seen map[string]bool
}

func newMemStore() *memStore {
	return &memStore{seen: make(map[string]bool)}
}

func (s *memStore) IsSeen(_ context.Context, repo, sha, prOrBranch string) (bool, error) {
	return s.seen[repo+"|"+sha+"|"+prOrBranch], nil
}

func (s *memStore) MarkSeen(_ context.Context, sc store.SeenCommit) error {
	s.seen[sc.Repo+"|"+sc.SHA+"|"+sc.PrOrBranch] = true
	return nil
}

func (s *memStore) CreateRun(_ context.Context, _ *store.Run) error          { return nil }
func (s *memStore) UpdateRunStatus(_ context.Context, _ string, _ store.RunStatus, _ *int, _ *time.Time) error {
	return nil
}
func (s *memStore) GetRun(_ context.Context, _ string) (*store.Run, error)   { return nil, nil }
func (s *memStore) ListRuns(_ context.Context, _ int) ([]store.Run, error)   { return nil, nil }
func (s *memStore) DeleteRun(_ context.Context, _ string) error               { return nil }
func (s *memStore) RunExists(_ context.Context, _, _, _ string) (bool, error) { return false, nil }
func (s *memStore) Close() error                                               { return nil }

// newGHClient creates a github.Client pointed at the given test server URL.
func newGHClient(serverURL string) *github.Client {
	u, _ := url.Parse(serverURL + "/")
	client := github.NewClient(nil)
	client.BaseURL = u
	client.UploadURL = u
	return client
}

// newTestConfig builds a minimal config for a branch watch.
func newBranchConfig(repo, branch string, interval time.Duration) *config.Config {
	return &config.Config{
		GitHub: config.GitHubConfig{PollInterval: interval},
		Watches: []config.Watch{
			{
				Name:   "test-watch",
				Repo:   repo,
				Branch: branch,
				Workflows: []config.WorkflowRef{
					{Path: ".github/workflows/ci.yml"},
				},
			},
		},
	}
}

// newPRConfig builds a minimal config for a PR watch.
func newPRConfig(repo string, pr int, interval time.Duration) *config.Config {
	return &config.Config{
		GitHub: config.GitHubConfig{PollInterval: interval},
		Watches: []config.Watch{
			{
				Name: "pr-watch",
				Repo: repo,
				PR:   pr,
				Workflows: []config.WorkflowRef{
					{Path: ".github/workflows/ci.yml"},
				},
			},
		},
	}
}

func silentLogger() zerolog.Logger {
	return zerolog.New(io.Discard)
}

// TestPoller_NewSHA_Branch verifies that a new SHA on a branch emits a Job.
func TestPoller_NewSHA_Branch(t *testing.T) {
	const sha = "deadbeef1234"
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ref": "refs/heads/main",
			"object": map[string]any{
				"sha":  sha,
				"type": "commit",
				"url":  "https://api.github.com/repos/owner/repo/git/commits/" + sha,
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := newMemStore()
	jobs := make(chan poller.Job, 1)
	cfg := newBranchConfig("owner/repo", "main", 10*time.Millisecond)
	p := poller.New(cfg, newGHClient(ts.URL), st, jobs, silentLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go p.Run(ctx)

	select {
	case job := <-jobs:
		assert.Equal(t, sha, job.SHA)
		assert.Equal(t, "owner/repo", job.Repo)
		assert.Equal(t, "owner", job.Owner)
		assert.Equal(t, "repo", job.RepoName)
		assert.Equal(t, "main", job.Branch)
		assert.Equal(t, 0, job.PRNumber)
		assert.Equal(t, "push", job.EventType)
		assert.Equal(t, "test-watch", job.WatchName)
	case <-ctx.Done():
		t.Fatal("timed out waiting for job")
	}
}

// TestPoller_DuplicateSHA_NoJob verifies that the same SHA on a second poll emits no Job.
func TestPoller_DuplicateSHA_NoJob(t *testing.T) {
	const sha = "cafebabe5678"
	var callCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ref": "refs/heads/main",
			"object": map[string]any{
				"sha":  sha,
				"type": "commit",
				"url":  "https://api.github.com/repos/owner/repo/git/commits/" + sha,
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := newMemStore()
	jobs := make(chan poller.Job, 10)
	cfg := newBranchConfig("owner/repo", "main", 20*time.Millisecond)
	p := poller.New(cfg, newGHClient(ts.URL), st, jobs, silentLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go p.Run(ctx)

	// Collect any jobs that arrive.
	<-ctx.Done()

	// We should have received exactly 1 job despite multiple polls.
	assert.Equal(t, 1, len(jobs))

	// Server must have been called more than once (proving polling happened).
	assert.Greater(t, callCount.Load(), int32(1))
}

// TestPoller_PR_NewSHA verifies that a new SHA on a PR emits a Job with PR metadata.
func TestPoller_PR_NewSHA(t *testing.T) {
	const sha = "pr-sha-abcd"
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/pulls/42/commits", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"sha": sha, "commit": map[string]any{"message": "feat: something"}},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := newMemStore()
	jobs := make(chan poller.Job, 1)
	cfg := newPRConfig("owner/repo", 42, 10*time.Millisecond)
	p := poller.New(cfg, newGHClient(ts.URL), st, jobs, silentLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go p.Run(ctx)

	select {
	case job := <-jobs:
		assert.Equal(t, sha, job.SHA)
		assert.Equal(t, 42, job.PRNumber)
		assert.Equal(t, "pull_request", job.EventType)
		assert.Equal(t, "pr-watch", job.WatchName)
	case <-ctx.Done():
		t.Fatal("timed out waiting for PR job")
	}
}

// TestPoller_RateLimit_BacksOffAndRetries verifies that a 429 causes a backoff and the
// poller retries successfully on the next attempt.
func TestPoller_RateLimit_BacksOffAndRetries(t *testing.T) {
	const sha = "rate-limited-sha"
	var callCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/git/ref/heads/main", func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			// First call: rate limit response.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		// Subsequent calls: valid response.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ref": "refs/heads/main",
			"object": map[string]any{
				"sha":  sha,
				"type": "commit",
				"url":  "https://api.github.com/repos/owner/repo/git/commits/" + sha,
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	st := newMemStore()
	jobs := make(chan poller.Job, 1)
	cfg := newBranchConfig("owner/repo", "main", 10*time.Millisecond)
	p := poller.New(cfg, newGHClient(ts.URL), st, jobs, silentLogger())
	// Override initial backoff so the test runs quickly.
	p.SetInitialBackoff(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go p.Run(ctx)

	select {
	case job := <-jobs:
		assert.Equal(t, sha, job.SHA)
		// Server was called at least twice: once for the rate limit, once for the success.
		require.GreaterOrEqual(t, callCount.Load(), int32(2))
	case <-ctx.Done():
		t.Fatalf("timed out waiting for job after rate limit; server called %d times", callCount.Load())
	}
}
