package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compositeActionYAML is a minimal composite action.yml for tests.
const compositeActionYAML = `
name: test-composite
description: A composite action for tests
inputs:
  greeting:
    description: Greeting word
    default: hello
outputs:
  message:
    description: The message
    value: ${{ steps.greet.outputs.msg }}
runs:
  using: composite
  steps:
    - id: greet
      run: echo "hello"
      shell: bash
`

// jsActionYAML is a minimal node20 action.yml for tests.
const jsActionYAML = `
name: test-js
description: A JavaScript action for tests
runs:
  using: node20
  main: dist/index.js
`

// TestResolveAction_Docker verifies that docker:// references resolve without network access.
func TestResolveAction_Docker(t *testing.T) {
	resolved, err := ResolveAction(context.Background(), "docker://alpine:3.19", "", "")
	require.NoError(t, err)
	assert.Equal(t, ActionTypeDocker, resolved.Type)
	assert.Equal(t, "alpine:3.19", resolved.Image)
	assert.Nil(t, resolved.Def)
	assert.Equal(t, "docker://alpine:3.19", resolved.Uses)
}

// TestResolveAction_Docker_NoTag verifies that docker:// without a tag also works.
func TestResolveAction_Docker_NoTag(t *testing.T) {
	resolved, err := ResolveAction(context.Background(), "docker://ghcr.io/org/image", "", "")
	require.NoError(t, err)
	assert.Equal(t, ActionTypeDocker, resolved.Type)
	assert.Equal(t, "ghcr.io/org/image", resolved.Image)
}

// TestResolveAction_Remote_Composite uses a mock HTTP server to verify
// that owner/repo@ref with composite action.yml resolves to ActionTypeComposite.
func TestResolveAction_Remote_Composite(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(compositeActionYAML))
	}))
	defer server.Close()

	old := githubRawBaseURL
	githubRawBaseURL = server.URL
	defer func() { githubRawBaseURL = old }()

	cacheDir := t.TempDir()
	resolved, err := ResolveAction(context.Background(), "owner/repo@v1", "", cacheDir)
	require.NoError(t, err)

	assert.Equal(t, ActionTypeComposite, resolved.Type)
	assert.NotNil(t, resolved.Def)
	assert.Equal(t, "composite", resolved.Def.Runs.Using)
	assert.Equal(t, "owner/repo@v1", resolved.Uses)
	assert.NotEmpty(t, resolved.SourceDir)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requestCount), "should have made exactly one HTTP request")
}

// TestResolveAction_Remote_JS uses a mock HTTP server to verify
// that owner/repo@ref with a node20 action.yml resolves to ActionTypeJS.
func TestResolveAction_Remote_JS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(jsActionYAML))
	}))
	defer server.Close()

	old := githubRawBaseURL
	githubRawBaseURL = server.URL
	defer func() { githubRawBaseURL = old }()

	cacheDir := t.TempDir()
	resolved, err := ResolveAction(context.Background(), "owner/repo@v1", "", cacheDir)
	require.NoError(t, err)

	assert.Equal(t, ActionTypeJS, resolved.Type)
	assert.NotNil(t, resolved.Def)
	assert.Equal(t, "node20", resolved.Def.Runs.Using)
}

// TestResolveAction_CacheHit verifies that a second call with the same owner/repo@ref
// reads from disk and does not make a network request.
func TestResolveAction_CacheHit(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(compositeActionYAML))
	}))
	defer server.Close()

	old := githubRawBaseURL
	githubRawBaseURL = server.URL
	defer func() { githubRawBaseURL = old }()

	cacheDir := t.TempDir()

	// First call — hits the mock server.
	r1, err := ResolveAction(context.Background(), "owner/repo@v2", "", cacheDir)
	require.NoError(t, err)
	assert.Equal(t, ActionTypeComposite, r1.Type)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requestCount), "first call should hit network")

	// Second call — should read from cache, not the network.
	r2, err := ResolveAction(context.Background(), "owner/repo@v2", "", cacheDir)
	require.NoError(t, err)
	assert.Equal(t, ActionTypeComposite, r2.Type)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requestCount), "second call must not hit network (cache hit)")

	// Cached file should exist on disk.
	cachedFile := filepath.Join(cacheDir, "owner", "repo", "v2", "action.yml")
	_, err = os.Stat(cachedFile)
	assert.NoError(t, err, "cached action.yml should exist on disk")
}

// TestResolveAction_Local_Composite verifies that ./local/path with a composite
// action.yml resolves to ActionTypeLocal.
func TestResolveAction_Local_Composite(t *testing.T) {
	workspaceDir := t.TempDir()

	// Create the local action directory with action.yml.
	actionDir := filepath.Join(workspaceDir, "local-action")
	require.NoError(t, os.MkdirAll(actionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(compositeActionYAML), 0o644))

	resolved, err := ResolveAction(context.Background(), "./local-action", workspaceDir, "")
	require.NoError(t, err)

	assert.Equal(t, ActionTypeLocal, resolved.Type)
	assert.NotNil(t, resolved.Def)
	assert.Equal(t, "composite", resolved.Def.Runs.Using)
	assert.Equal(t, actionDir, resolved.SourceDir)
	assert.Equal(t, "./local-action", resolved.Uses)
}

// TestResolveAction_Local_JS verifies that ./local/path with a node20 action.yml
// resolves to ActionTypeJS.
func TestResolveAction_Local_JS(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "js-action")
	require.NoError(t, os.MkdirAll(actionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(jsActionYAML), 0o644))

	resolved, err := ResolveAction(context.Background(), "./js-action", workspaceDir, "")
	require.NoError(t, err)

	assert.Equal(t, ActionTypeJS, resolved.Type)
}

// TestResolveAction_Local_ActionYamlFallback verifies that action.yaml (without l)
// is also accepted.
func TestResolveAction_Local_ActionYamlFallback(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "fallback-action")
	require.NoError(t, os.MkdirAll(actionDir, 0o755))
	// Use .yaml extension instead of .yml.
	require.NoError(t, os.WriteFile(filepath.Join(actionDir, "action.yaml"), []byte(compositeActionYAML), 0o644))

	resolved, err := ResolveAction(context.Background(), "./fallback-action", workspaceDir, "")
	require.NoError(t, err)

	assert.Equal(t, ActionTypeLocal, resolved.Type)
}

// TestResolveAction_Remote_404_BothExtensions verifies that a 404 for both
// action.yml and action.yaml returns an error.
func TestResolveAction_Remote_404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	old := githubRawBaseURL
	githubRawBaseURL = server.URL
	defer func() { githubRawBaseURL = old }()

	_, err := ResolveAction(context.Background(), "owner/repo@v1", "", t.TempDir())
	assert.Error(t, err)
}

// TestResolveAction_InvalidUses verifies that malformed uses: strings return errors.
func TestResolveAction_InvalidUses(t *testing.T) {
	_, err := ResolveAction(context.Background(), "noslash-noat", "", t.TempDir())
	assert.Error(t, err)

	_, err = ResolveAction(context.Background(), "noat/here", "", t.TempDir())
	assert.Error(t, err)
}

// TestNeedsClone_WithGitDir verifies that a dir containing .git does not need cloning.
func TestNeedsClone_WithGitDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))

	assert.False(t, needsClone(dir), "dir with .git should not need clone")
}

// TestNeedsClone_OnlyActionYML verifies that a dir with only action.yml needs cloning.
func TestNeedsClone_OnlyActionYML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "action.yml"), []byte("name: x"), 0o644))

	assert.True(t, needsClone(dir), "dir with only action.yml should need clone")
}

// TestNeedsClone_MultipleFiles verifies that a dir with multiple files does not need cloning.
func TestNeedsClone_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "action.yml"), []byte("name: x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entrypoint.sh"), []byte("#!/bin/sh"), 0o755))

	assert.False(t, needsClone(dir), "dir with multiple files should not need clone")
}

// TestExecActionStep_JSReturnsError verifies that JS/Dockerfile actions return a clear error.
func TestExecActionStep_JSReturnsError(t *testing.T) {
	r := &podmanJobRunner{}
	step := &Step{Uses: "actions/setup-node@v4"}
	resolved := &ResolvedAction{
		Type: ActionTypeJS,
		Def:  &ActionDef{Runs: ActionRuns{Using: "node20"}},
		Uses: "actions/setup-node@v4",
	}
	execCtx := &ExecutionContext{
		Eval:        &Evaluator{},
		StepResults: make(map[string]StepResult),
		LiveEnv:     make(map[string]string),
		Secrets:     make(map[string]string),
		Inputs:      make(map[string]string),
	}

	result, err := r.ExecActionStep(
		context.Background(), step, resolved, nil, execCtx,
		JobInfo{}, nil, context.Background(), nil,
	)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported in ghenkins MVP")
	assert.Equal(t, "failure", result.Outcome)
}

// TestExecActionStep_DockerfileReturnsError verifies Dockerfile-based actions return an error.
func TestExecActionStep_DockerfileReturnsError(t *testing.T) {
	r := &podmanJobRunner{}
	step := &Step{Uses: "owner/docker-action@v1"}
	resolved := &ResolvedAction{
		Type: ActionTypeDockerfile,
		Def:  &ActionDef{Runs: ActionRuns{Using: "docker", Image: "Dockerfile"}},
		Uses: "owner/docker-action@v1",
	}
	execCtx := &ExecutionContext{
		Eval:        &Evaluator{},
		StepResults: make(map[string]StepResult),
		LiveEnv:     make(map[string]string),
		Secrets:     make(map[string]string),
		Inputs:      make(map[string]string),
	}

	result, err := r.ExecActionStep(
		context.Background(), step, resolved, nil, execCtx,
		JobInfo{}, nil, context.Background(), nil,
	)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported in ghenkins MVP")
	assert.Equal(t, "failure", result.Outcome)
}
