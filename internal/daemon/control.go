package daemon

import "context"

// RunControl allows the HTTP server to trigger and cancel runs.
type RunControl interface {
	TriggerRun(ctx context.Context, projectName, workflowName string) (runID string, err error)
	CancelRun(ctx context.Context, runID string) error
}
