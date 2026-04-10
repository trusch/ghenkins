package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) Store {
	t.Helper()
	s, err := Open("file::memory:?cache=shared&mode=memory")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndGetRun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	run := &Run{
		ID:           "run-1",
		WatchName:    "my-watch",
		Repo:         "owner/repo",
		SHA:          "abc123",
		WorkflowName: "ci.yml",
		Status:       RunStatusQueued,
		StartedAt:    now,
		LogPath:      "/tmp/run-1.log",
	}

	require.NoError(t, s.CreateRun(ctx, run))

	got, err := s.GetRun(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, run.ID, got.ID)
	assert.Equal(t, run.WatchName, got.WatchName)
	assert.Equal(t, run.Repo, got.Repo)
	assert.Equal(t, run.SHA, got.SHA)
	assert.Equal(t, run.WorkflowName, got.WorkflowName)
	assert.Equal(t, run.Status, got.Status)
	assert.Equal(t, run.StartedAt.Unix(), got.StartedAt.Unix())
	assert.Nil(t, got.FinishedAt)
	assert.Nil(t, got.ExitCode)
	assert.Equal(t, run.LogPath, got.LogPath)
}

func TestUpdateRunStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	run := &Run{
		ID:           "run-2",
		WatchName:    "w",
		Repo:         "owner/repo",
		SHA:          "def456",
		WorkflowName: "test.yml",
		Status:       RunStatusQueued,
		StartedAt:    now,
	}
	require.NoError(t, s.CreateRun(ctx, run))

	// Transition to running
	require.NoError(t, s.UpdateRunStatus(ctx, "run-2", RunStatusRunning, nil, nil))
	got, err := s.GetRun(ctx, "run-2")
	require.NoError(t, err)
	assert.Equal(t, RunStatusRunning, got.Status)
	assert.Nil(t, got.ExitCode)
	assert.Nil(t, got.FinishedAt)

	// Transition to success
	exitCode := 0
	finishedAt := now.Add(10 * time.Second)
	require.NoError(t, s.UpdateRunStatus(ctx, "run-2", RunStatusSuccess, &exitCode, &finishedAt))
	got, err = s.GetRun(ctx, "run-2")
	require.NoError(t, err)
	assert.Equal(t, RunStatusSuccess, got.Status)
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 0, *got.ExitCode)
	require.NotNil(t, got.FinishedAt)
	assert.Equal(t, finishedAt.Unix(), got.FinishedAt.Unix())
}

func TestRunExists(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// No runs yet
	exists, err := s.RunExists(ctx, "owner/repo", "sha1", "ci.yml")
	require.NoError(t, err)
	assert.False(t, exists)

	// Create queued run
	run := &Run{
		ID: "run-3", WatchName: "w", Repo: "owner/repo", SHA: "sha1",
		WorkflowName: "ci.yml", Status: RunStatusQueued, StartedAt: now,
	}
	require.NoError(t, s.CreateRun(ctx, run))

	exists, err = s.RunExists(ctx, "owner/repo", "sha1", "ci.yml")
	require.NoError(t, err)
	assert.True(t, exists)

	// Update to running — still active
	require.NoError(t, s.UpdateRunStatus(ctx, "run-3", RunStatusRunning, nil, nil))
	exists, err = s.RunExists(ctx, "owner/repo", "sha1", "ci.yml")
	require.NoError(t, err)
	assert.True(t, exists)

	// Complete it — no longer active
	exitCode := 0
	finished := now.Add(5 * time.Second)
	require.NoError(t, s.UpdateRunStatus(ctx, "run-3", RunStatusSuccess, &exitCode, &finished))
	exists, err = s.RunExists(ctx, "owner/repo", "sha1", "ci.yml")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestMarkSeenAndIsSeen(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	sc := SeenCommit{
		Repo:        "owner/repo",
		SHA:         "abc",
		PrOrBranch:  "main",
		FirstSeenAt: time.Now().UTC(),
	}

	seen, err := s.IsSeen(ctx, sc.Repo, sc.SHA, sc.PrOrBranch)
	require.NoError(t, err)
	assert.False(t, seen)

	require.NoError(t, s.MarkSeen(ctx, sc))

	seen, err = s.IsSeen(ctx, sc.Repo, sc.SHA, sc.PrOrBranch)
	require.NoError(t, err)
	assert.True(t, seen)

	// Duplicate MarkSeen should not error
	require.NoError(t, s.MarkSeen(ctx, sc))

	seen, err = s.IsSeen(ctx, sc.Repo, sc.SHA, sc.PrOrBranch)
	require.NoError(t, err)
	assert.True(t, seen)

	// Different pr_or_branch is not seen
	seen, err = s.IsSeen(ctx, sc.Repo, sc.SHA, "feature-branch")
	require.NoError(t, err)
	assert.False(t, seen)
}

func TestListRuns(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	ids := []string{"run-a", "run-b", "run-c"}
	for i, id := range ids {
		run := &Run{
			ID:           id,
			WatchName:    "w",
			Repo:         "owner/repo",
			SHA:          id,
			WorkflowName: "ci.yml",
			Status:       RunStatusQueued,
			StartedAt:    base.Add(time.Duration(i) * time.Second),
		}
		require.NoError(t, s.CreateRun(ctx, run))
	}

	runs, err := s.ListRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 3)

	// Newest first
	assert.Equal(t, "run-c", runs[0].ID)
	assert.Equal(t, "run-b", runs[1].ID)
	assert.Equal(t, "run-a", runs[2].ID)

	// Limit respected
	runs, err = s.ListRuns(ctx, 2)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, "run-c", runs[0].ID)
}

func TestDeleteRun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	run := &Run{
		ID:           "run-del",
		WatchName:    "w",
		Repo:         "owner/repo",
		SHA:          "xyz",
		WorkflowName: "ci.yml",
		Status:       RunStatusQueued,
		StartedAt:    time.Now().UTC(),
	}
	require.NoError(t, s.CreateRun(ctx, run))

	got, err := s.GetRun(ctx, "run-del")
	require.NoError(t, err)
	assert.Equal(t, "run-del", got.ID)

	require.NoError(t, s.DeleteRun(ctx, "run-del"))

	_, err = s.GetRun(ctx, "run-del")
	assert.Error(t, err)
}
