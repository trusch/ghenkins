//go:build integration

package runner

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSmokeRealWorkflow exercises the full path:
// parse workflow → Podman container → run bash steps → verify exit code.
// Skipped gracefully if Podman is not available.
func TestSmokeRealWorkflow(t *testing.T) {
	ctx := context.Background()

	conn, err := Connect(ctx)
	if err != nil {
		t.Skipf("Podman socket not available: %v", err)
	}

	workDir := t.TempDir()
	cacheDir := t.TempDir()

	wfContent := `name: Smoke Test
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: Print env
        run: |
          echo "SHA=$GITHUB_SHA"
          echo "REPO=$GITHUB_REPOSITORY"
          echo "CI=$CI"
      - id: write-output
        name: Write output
        run: echo "result=42" >> $GITHUB_OUTPUT
      - name: Read output via env
        run: echo "RESULT=${{ steps.write-output.outputs.result }}"
      - name: Conditional step
        if: success()
        run: echo "all good"
`
	workflow, err := ParseWorkflow([]byte(wfContent))
	require.NoError(t, err)

	info := JobInfo{
		SHA:       "abc1234",
		Ref:       "refs/heads/main",
		RefName:   "main",
		EventName: "push",
		Repo:      "test/test",
		Owner:     "test",
		RepoName:  "test",
		RunID:     "smoke-run-1",
		RunNumber: 1,
		Actor:     "ghenkins",
	}

	jobRunner := &podmanJobRunner{
		conn:         conn,
		WorkspaceDir: workDir,
		cacheDir:     cacheDir,
	}

	var logBuf bytes.Buffer
	result, err := jobRunner.RunJob(ctx, "build", workflow.Jobs["build"], workflow, info, nil, &logBuf, "ubuntu:22.04")
	require.NoError(t, err)
	assert.Equal(t, JobStatusSuccess, result.Status, "job should succeed; log:\n%s", logBuf.String())
}
