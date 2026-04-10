package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildEvalContext_SHA verifies that BuildEvalContext populates github.sha from JobInfo.
func TestBuildEvalContext_SHA(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{}
	info := &JobInfo{
		SHA:       "abc123def456",
		Ref:       "refs/heads/main",
		RefName:   "main",
		EventName: "push",
		Repo:      "owner/repo",
		Owner:     "owner",
		Actor:     "ghenkins",
	}

	ctx := BuildEvalContext(wf, "build", job, nil, nil, nil, info, nil, JobStatusPending, false)

	require.NotNil(t, ctx)
	assert.Equal(t, "abc123def456", ctx.Github["sha"])
	assert.Equal(t, "refs/heads/main", ctx.Github["ref"])
	assert.Equal(t, "main", ctx.Github["ref_name"])
	assert.Equal(t, "owner/repo", ctx.Github["repository"])
	assert.Equal(t, "build", ctx.Github["job"])
}

// TestBuildShellCmd_Bash verifies the bash command structure.
func TestBuildShellCmd_Bash(t *testing.T) {
	script := "echo hello"
	cmd := buildShellCmd("bash", script)

	require.Equal(t, []string{"/bin/bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script}, cmd)
}

// TestBuildShellCmd_Sh verifies the sh command structure.
func TestBuildShellCmd_Sh(t *testing.T) {
	script := "echo hello"
	cmd := buildShellCmd("sh", script)

	require.Equal(t, []string{"/bin/sh", "-e", "-c", script}, cmd)
}

// TestBuildShellCmd_Empty defaults to bash.
func TestBuildShellCmd_Empty(t *testing.T) {
	cmd := buildShellCmd("", "echo hi")
	assert.Equal(t, "/bin/bash", cmd[0])
}

// TestRunJob_IfFalse_SkipsAllSteps verifies that a job with if: false returns
// immediately without executing any steps (and without needing a Podman connection).
func TestRunJob_IfFalse_SkipsAllSteps(t *testing.T) {
	r := &podmanJobRunner{} // nil conn — must not be reached

	wf := &Workflow{Name: "ci"}
	job := &Job{
		If: "false",
		Steps: []*Step{
			{ID: "s1", Run: "exit 1"},
		},
	}
	info := JobInfo{
		SHA:       "deadbeef",
		Ref:       "refs/heads/main",
		RefName:   "main",
		EventName: "push",
		Repo:      "owner/repo",
		Owner:     "owner",
	}

	result, err := r.RunJob(context.Background(), "build", job, wf, info, nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, JobStatusSuccess, result.Status)
	assert.Empty(t, result.Steps) // no steps executed
}

// TestStepCondition_FailureSkippedWhenPassing verifies failure() is false when job is passing.
func TestStepCondition_FailureSkippedWhenPassing(t *testing.T) {
	eval := &Evaluator{}
	ctx := &EvalContext{JobStatus: JobStatusPending}

	ok, err := eval.EvalBool("failure()", ctx)
	require.NoError(t, err)
	assert.False(t, ok, "failure() should be false when job is passing")
}

// TestStepCondition_AlwaysRunsAfterFailure verifies always() is true regardless of job status.
func TestStepCondition_AlwaysRunsAfterFailure(t *testing.T) {
	eval := &Evaluator{}

	for _, status := range []JobStatus{JobStatusPending, JobStatusSuccess, JobStatusFailure, JobStatusCancelled} {
		ctx := &EvalContext{JobStatus: status}
		ok, err := eval.EvalBool("always()", ctx)
		require.NoError(t, err)
		assert.True(t, ok, "always() should be true for status %v", status)
	}
}
