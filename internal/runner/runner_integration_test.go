//go:build integration

package runner

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPodmanRunner_SimpleRunStep is a smoke-test that runs a single "run: echo hello"
// step end-to-end via podmanJobRunner. Skipped gracefully if Podman is not available.
func TestPodmanRunner_SimpleRunStep(t *testing.T) {
	ctx := context.Background()

	// Check if Podman is reachable; skip if not.
	conn, err := Connect(ctx)
	if err != nil {
		t.Skipf("Podman socket not available: %v", err)
	}

	workDir := t.TempDir()
	cacheDir := t.TempDir()

	// Write a minimal workflow YAML inside workDir so the path exists.
	wfDir := workDir + "/.github/workflows"
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	wfContent := `name: CI
on: [push]
jobs:
  hello:
    runs-on: ubuntu:22.04
    steps:
      - name: Say hello
        run: echo hello
`
	require.NoError(t, os.WriteFile(wfDir+"/ci.yml", []byte(wfContent), 0o644))

	workflow, err := ParseWorkflow([]byte(wfContent))
	require.NoError(t, err)

	info := JobInfo{
		SHA:       "deadbeef",
		Ref:       "refs/heads/main",
		RefName:   "main",
		EventName: "push",
		Repo:      "owner/repo",
		Owner:     "owner",
		RepoName:  "repo",
		RunID:     "test-run-1",
		RunNumber: 1,
		Actor:     "ghenkins",
	}

	jobRunner := &podmanJobRunner{
		conn:         conn,
		WorkspaceDir: workDir,
		cacheDir:     cacheDir,
	}

	var logBuf bytes.Buffer
	result, err := jobRunner.RunJob(ctx, "hello", workflow.Jobs["hello"], workflow, info, nil, &logBuf, "ubuntu:22.04")
	require.NoError(t, err)
	assert.Equal(t, JobStatusSuccess, result.Status, "job should succeed; log:\n%s", logBuf.String())
}
