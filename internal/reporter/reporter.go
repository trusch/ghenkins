package reporter

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v67/github"
	"github.com/rs/zerolog"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusSuccess Status = "success"
	StatusFailure Status = "failure"
	StatusError   Status = "error"
)

type ReportRequest struct {
	Owner        string
	Repo         string
	SHA          string
	WorkflowName string
	Status       Status
	Description  string
	TargetURL    string
}

type Reporter interface {
	Report(ctx context.Context, r ReportRequest) error
}

type GitHubReporter struct {
	client *github.Client
	log    zerolog.Logger
}

func New(client *github.Client, log zerolog.Logger) *GitHubReporter {
	return &GitHubReporter{client: client, log: log}
}

func (r *GitHubReporter) Report(ctx context.Context, req ReportRequest) error {
	desc := req.Description
	if len(desc) > 140 {
		desc = desc[:140]
	}

	context := "ghenkins/" + req.WorkflowName
	state := string(req.Status)

	status := &github.RepoStatus{
		State:       &state,
		Description: &desc,
		Context:     &context,
	}
	if req.TargetURL != "" {
		status.TargetURL = &req.TargetURL
	}

	backoffs := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoffs[attempt-1]):
			}
		}

		_, resp, err := r.client.Repositories.CreateStatus(ctx, req.Owner, req.Repo, req.SHA, status)
		if err == nil {
			return nil
		}

		lastErr = err

		if resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			// 4xx: do not retry
			return fmt.Errorf("github status API 4xx (%d): %w", resp.StatusCode, err)
		}

		if resp != nil && resp.StatusCode >= 500 {
			r.log.Warn().Err(err).Int("attempt", attempt+1).Msg("github status API 5xx, retrying")
			continue
		}

		// Network error or other — retry
		r.log.Warn().Err(err).Int("attempt", attempt+1).Msg("github status API error, retrying")
	}

	return fmt.Errorf("github status API failed after retries: %w", lastErr)
}

