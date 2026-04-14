package poller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v67/github"
	"github.com/rs/zerolog"
	"github.com/trusch/ghenkins/internal/config"
	"github.com/trusch/ghenkins/internal/store"
)

// Job is emitted by the poller when a new SHA is detected.
type Job struct {
	WatchName    string
	Repo         string // "owner/repo"
	Owner        string
	RepoName     string
	SHA          string
	PRNumber     int    // 0 if branch-based
	Branch       string // "" if PR-based
	EventType    string // "push" | "pull_request"
	WorkflowRefs []config.WorkflowRef
	Inputs       map[string]string // caller-provided inputs for manual triggers
	RunID        string            // pre-assigned run ID for manual triggers; empty = auto-generate
}

type WatchProvider func(ctx context.Context) ([]config.Watch, error)

type Poller struct {
	cfg            *config.Config
	ghClient       *github.Client
	store          store.Store
	jobs           chan<- Job
	log            zerolog.Logger
	initialBackoff time.Duration
	watchProvider  WatchProvider
}

func New(cfg *config.Config, gh *github.Client, st store.Store, jobs chan<- Job, log zerolog.Logger) *Poller {
	return &Poller{
		cfg:            cfg,
		ghClient:       gh,
		store:          st,
		jobs:           jobs,
		log:            log,
		initialBackoff: 5 * time.Second,
	}
}

// SetWatchProvider sets a function that returns the current list of watches.
// When set, this overrides cfg.Watches in Run().
func (p *Poller) SetWatchProvider(fn WatchProvider) {
	p.watchProvider = fn
}

// SetInitialBackoff overrides the starting backoff duration (default 5s). Useful in tests.
func (p *Poller) SetInitialBackoff(d time.Duration) {
	p.initialBackoff = d
}

// Run starts one goroutine per watch and blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) error {
	watches := p.cfg.Watches
	if p.watchProvider != nil {
		var err error
		watches, err = p.watchProvider(ctx)
		if err != nil {
			p.log.Error().Err(err).Msg("failed to load watches from provider")
			watches = p.cfg.Watches
		}
	}

	var wg sync.WaitGroup
	for _, w := range watches {
		wg.Add(1)
		go func(w config.Watch) {
			defer wg.Done()
			p.runWatch(ctx, w)
		}(w)
	}
	wg.Wait()
	return nil
}

func (p *Poller) runWatch(ctx context.Context, w config.Watch) {
	parts := strings.SplitN(w.Repo, "/", 2)
	if len(parts) != 2 {
		p.log.Error().Str("repo", w.Repo).Msg("invalid repo format")
		return
	}
	owner, repoName := parts[0], parts[1]

	prOrBranch := w.Branch
	if w.PR != 0 {
		prOrBranch = fmt.Sprintf("pr/%d", w.PR)
	}

	interval := p.cfg.GitHub.PollInterval
	if interval == 0 {
		interval = 30 * time.Second
	}

	backoff := time.Duration(0)

	for {
		wait := interval
		if backoff > 0 {
			wait = backoff
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		sha, err := p.fetchSHA(ctx, owner, repoName, w)
		if err != nil {
			if isRateLimitErr(err) {
				backoff = p.nextBackoff(backoff)
				p.log.Warn().Dur("backoff", backoff).Str("watch", w.Name).Msg("rate limited, backing off")
			} else {
				backoff = 0
				p.log.Error().Err(err).Str("watch", w.Name).Msg("failed to fetch SHA")
			}
			continue
		}
		backoff = 0

		seen, err := p.store.IsSeen(ctx, w.Repo, sha, prOrBranch)
		if err != nil {
			p.log.Error().Err(err).Str("watch", w.Name).Msg("failed to check seen")
			continue
		}
		if seen {
			continue
		}

		if err := p.store.MarkSeen(ctx, store.SeenCommit{
			Repo:        w.Repo,
			SHA:         sha,
			PrOrBranch:  prOrBranch,
			FirstSeenAt: time.Now().UTC(),
		}); err != nil {
			p.log.Error().Err(err).Str("watch", w.Name).Msg("failed to mark seen")
			continue
		}

		eventType := "push"
		if w.PR != 0 {
			eventType = "pull_request"
		}

		job := Job{
			WatchName:    w.Name,
			Repo:         w.Repo,
			Owner:        owner,
			RepoName:     repoName,
			SHA:          sha,
			PRNumber:     w.PR,
			Branch:       w.Branch,
			EventType:    eventType,
			WorkflowRefs: w.Workflows,
		}

		select {
		case p.jobs <- job:
		default:
			p.log.Warn().Str("watch", w.Name).Msg("jobs channel full, dropping job")
		}
	}
}

func (p *Poller) fetchSHA(ctx context.Context, owner, repo string, w config.Watch) (string, error) {
	if w.PR != 0 {
		commits, _, err := p.ghClient.PullRequests.ListCommits(ctx, owner, repo, w.PR, &github.ListOptions{PerPage: 1})
		if err != nil {
			return "", err
		}
		if len(commits) == 0 {
			return "", fmt.Errorf("no commits found for PR %d", w.PR)
		}
		return commits[len(commits)-1].GetSHA(), nil
	}
	ref, _, err := p.ghClient.Git.GetRef(ctx, owner, repo, "heads/"+w.Branch)
	if err != nil {
		return "", err
	}
	return ref.GetObject().GetSHA(), nil
}

func (p *Poller) nextBackoff(current time.Duration) time.Duration {
	if current == 0 {
		return p.initialBackoff
	}
	next := current * 2
	if next > 5*time.Minute {
		return 5 * time.Minute
	}
	return next
}

func isRateLimitErr(err error) bool {
	var rl *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rl) || errors.As(err, &abuse) {
		return true
	}
	var errResp *github.ErrorResponse
	if errors.As(err, &errResp) {
		return errResp.Response != nil && errResp.Response.StatusCode == http.StatusTooManyRequests
	}
	return false
}
