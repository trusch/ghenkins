package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/trusch/ghenkins/internal/config"
	"github.com/trusch/ghenkins/internal/poller"
	"github.com/trusch/ghenkins/internal/secrets"
	"github.com/trusch/ghenkins/internal/store"
)

// Runner executes a single workflow run.
type Runner interface {
	Run(ctx context.Context, j poller.Job, wf config.WorkflowRef, logWriter io.Writer) (exitCode int, err error)
}

// ActRunner runs workflows via the `act` CLI tool.
type ActRunner struct {
	cacheDir string // ~/.cache/ghenkins/repos
	log      zerolog.Logger
}

// New creates an ActRunner using cacheDir for bare clone storage.
func New(cacheDir string, log zerolog.Logger) *ActRunner {
	return &ActRunner{cacheDir: cacheDir, log: log}
}

// Run executes the workflow described by wf for job j, writing output to logWriter.
// Returns the exit code of act (non-zero means workflow failure, not infra error).
func (r *ActRunner) Run(ctx context.Context, j poller.Job, wf config.WorkflowRef, logWriter io.Writer) (int, error) {
	// 1. Ensure bare clone exists / is up to date
	bareDir, err := ensureBareClone(ctx, r.cacheDir, j.Owner, j.RepoName)
	if err != nil {
		return 0, fmt.Errorf("ensureBareClone: %w", err)
	}

	// 2. Create a worktree at the target SHA
	wtDir, cleanup, err := addWorktree(ctx, bareDir, j.SHA)
	if err != nil {
		return 0, fmt.Errorf("addWorktree: %w", err)
	}
	defer cleanup()

	// 3. Write event JSON to a temp file
	eventJSON, err := GenerateEventJSON(j.Repo, j.Owner, j.RepoName, j.SHA, j.Branch, j.PRNumber, j.EventType)
	if err != nil {
		return 0, fmt.Errorf("GenerateEventJSON: %w", err)
	}
	eventFile, err := os.CreateTemp("", "ghenkins-event-*.json")
	if err != nil {
		return 0, fmt.Errorf("create event file: %w", err)
	}
	eventPath := eventFile.Name()
	defer os.Remove(eventPath)
	if _, err := eventFile.Write(eventJSON); err != nil {
		eventFile.Close()
		return 0, fmt.Errorf("write event file: %w", err)
	}
	eventFile.Close()

	// 4. Write secrets file
	secretsPath, err := writeSecretsFile(wf.Secrets)
	if err != nil {
		return 0, fmt.Errorf("writeSecretsFile: %w", err)
	}
	defer secureDelete(secretsPath)

	// 5. Build act command
	args := []string{
		j.EventType,
		"--eventpath", eventPath,
		"--secret-file", secretsPath,
		"--workflows", wf.Path,
		"--directory", wtDir,
		"--pull=false",
		"--no-cache-server",
	}
	for k, v := range wf.Env {
		args = append(args, "--env", k+"="+v)
	}

	cmd := exec.CommandContext(ctx, "act", args...)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter

	// 6. Run and return exit code
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := isExitError(err, &exitErr); ok {
			// Workflow failed — return exit code, not an infra error
			return exitErr.ExitCode(), nil
		}
		return 0, fmt.Errorf("exec act: %w", err)
	}
	return 0, nil
}

// JobCallback is called after each job completes or is skipped.
type JobCallback func(jobName string, status JobStatus, startedAt, finishedAt time.Time)

// PodmanRunner runs workflows natively in Podman containers.
type PodmanRunner struct {
	cacheDir     string // ~/.cache/ghenkins/repos
	podmanSock   string // empty = auto-detect
	defaultImage string // fallback image when runs-on is not a valid image
	log          zerolog.Logger
	JobCallback  JobCallback // called after each job completes (or is skipped)
	CurrentRunID string      // DB run ID; set by daemon before each Run() call
	Store        store.Store // store for upserting artifacts; set by daemon
}

// NewPodman creates a PodmanRunner. defaultImage is used when the workflow does not specify
// an image via runner_image and the runs-on label is not a container image.
func NewPodman(cacheDir, defaultImage string, log zerolog.Logger) *PodmanRunner {
	return &PodmanRunner{
		cacheDir:     cacheDir,
		defaultImage: defaultImage,
		log:          log,
	}
}

// Run executes the workflow described by wf for job j, writing output to logWriter.
// Returns the exit code (non-zero means workflow failure, not infra error).
func (r *PodmanRunner) Run(ctx context.Context, j poller.Job, wf config.WorkflowRef, logWriter io.Writer) (int, error) {
	// 1. Connect to Podman socket
	conn, err := Connect(ctx)
	if err != nil {
		return 0, fmt.Errorf("connect to podman: %w", err)
	}

	// 2. Ensure bare clone and create worktree (skipped for repo-less manual workflows)
	var wtDir string
	sha := j.SHA
	if j.Repo == "" {
		// No upstream repo — create a temp workspace and use "manual" as the SHA.
		tmp, mkErr := os.MkdirTemp("", "ghenkins-wt-*")
		if mkErr != nil {
			return 0, fmt.Errorf("create temp workspace: %w", mkErr)
		}
		defer os.RemoveAll(tmp)
		wtDir = tmp
		if sha == "manual" {
			sha = "manual"
		}
		fmt.Fprintf(logWriter, "## No upstream repo — running without checkout\n")
	} else {
		fmt.Fprintf(logWriter, "## Cloning/fetching %s/%s...\n", j.Owner, j.RepoName)
		bareDir, err := ensureBareClone(ctx, r.cacheDir, j.Owner, j.RepoName)
		if err != nil {
			fmt.Fprintf(logWriter, "## ERROR: clone/fetch failed: %v\n", err)
			return 0, fmt.Errorf("ensureBareClone: %w", err)
		}
		// Resolve "manual" to the actual branch HEAD SHA
		if sha == "manual" {
			ref := "refs/heads/" + j.Branch
			out, rerr := exec.CommandContext(ctx, "git", "-C", bareDir, "rev-parse", ref).CombinedOutput()
			if rerr != nil {
				fmt.Fprintf(logWriter, "## ERROR: resolve branch %s HEAD: %v\n%s\n", j.Branch, rerr, out)
				return 0, fmt.Errorf("resolve branch %s HEAD: %w\n%s", j.Branch, rerr, out)
			}
			sha = strings.TrimSpace(string(out))
			if r.Store != nil && r.CurrentRunID != "" {
				_ = r.Store.UpdateRunSHA(ctx, r.CurrentRunID, sha)
			}
		}
		shortSHA := sha
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		fmt.Fprintf(logWriter, "## Checking out %s...\n", shortSHA)
		var cleanup func()
		wtDir, cleanup, err = addWorktree(ctx, bareDir, sha)
		if err != nil {
			fmt.Fprintf(logWriter, "## ERROR: worktree checkout failed: %v\n", err)
			return 0, fmt.Errorf("addWorktree: %w", err)
		}
		defer cleanup()
		fmt.Fprintf(logWriter, "## Workspace ready at %s\n", wtDir)
	}

	// 3. Generate event JSON and write to temp file
	eventJSON, err := GenerateEventJSON(j.Repo, j.Owner, j.RepoName, j.SHA, j.Branch, j.PRNumber, j.EventType)
	if err != nil {
		return 0, fmt.Errorf("GenerateEventJSON: %w", err)
	}

	// 4. Read and parse the workflow YAML
	wfPath := wf.Path
	if !filepath.IsAbs(wfPath) {
		wfPath = filepath.Join(wtDir, wfPath)
	}
	wfData, err := os.ReadFile(wfPath)
	if err != nil {
		return 0, fmt.Errorf("read workflow %s: %w", wf.Path, err)
	}
	workflow, err := ParseWorkflow(wfData)
	if err != nil {
		return 0, fmt.Errorf("ParseWorkflow: %w", err)
	}

	// 5. Resolve secrets
	secretStore, err := secrets.New("ghenkins")
	if err != nil {
		return 0, fmt.Errorf("create secret store: %w", err)
	}
	resolvedSecrets, err := secrets.ResolveSecrets(ctx, wf.Secrets, secretStore)
	if err != nil {
		return 0, fmt.Errorf("resolve secrets: %w", err)
	}

	// 6. Determine image: wf.RunnerImage (config override) takes priority.
	// If unset, RunJob will use runs-on from the workflow YAML.
	// r.defaultImage is a last-resort fallback when neither is set.
	imageForRun := wf.RunnerImage

	// 7. Build JobInfo
	ref := "refs/heads/" + j.Branch
	if j.EventType == "pull_request" {
		ref = fmt.Sprintf("refs/pull/%d/head", j.PRNumber)
	}
	runID := r.CurrentRunID
	if runID == "" {
		runID = newRunID()
	}
	// Merge declared workflow input defaults with caller-provided values.
	mergedInputs := make(map[string]string)
	for name, decl := range workflow.Inputs {
		if decl.Default != "" {
			mergedInputs[name] = decl.Default
		}
	}
	for k, v := range j.Inputs {
		mergedInputs[k] = v
	}

	info := JobInfo{
		SHA:          j.SHA,
		Ref:          ref,
		RefName:      j.Branch,
		EventName:    j.EventType,
		Repo:         j.Repo,
		Owner:        j.Owner,
		RepoName:     j.RepoName,
		RunID:        runID,
		RunNumber:    1,
		Actor:        "ghenkins",
		EventPayload: eventJSON,
		PRNumber:     j.PRNumber,
		Inputs:       mergedInputs,
	}

	// 8. Build job execution order
	order, err := BuildExecutionOrder(workflow.Jobs)
	if err != nil {
		return 0, fmt.Errorf("BuildExecutionOrder: %w", err)
	}

	// 9. Create job runner
	jobRunner := &podmanJobRunner{conn: conn, WorkspaceDir: wtDir, cacheDir: r.cacheDir, store: r.Store}

	// 10. Execute jobs level by level (sequential within each level for MVP)
	exitCode := 0
	failedJobs := make(map[string]bool)
	for _, level := range order {
		for _, jobID := range level {
			job := workflow.Jobs[jobID]

			// Skip this job if any required dependency failed.
			depFailed := false
			for _, need := range job.NeedsIDs() {
				if failedJobs[need] {
					depFailed = true
					break
				}
			}
			if depFailed {
				fmt.Fprintf(logWriter, "## skipping job %s: a required dependency failed\n", jobID)
				if r.JobCallback != nil {
					r.JobCallback(jobID, JobStatusSkipped, time.Time{}, time.Time{})
				}
				continue
			}

			jobStartedAt := time.Now()
			if r.JobCallback != nil {
				r.JobCallback(jobID, JobStatusRunning, jobStartedAt, time.Time{})
			}
			result, err := jobRunner.RunJob(ctx, jobID, job, workflow, info, resolvedSecrets, logWriter, imageForRun)
			jobFinishedAt := time.Now()
			if err != nil {
				if r.JobCallback != nil {
					r.JobCallback(jobID, JobStatusFailure, jobStartedAt, jobFinishedAt)
				}
				return 0, fmt.Errorf("RunJob %s: %w", jobID, err)
			}
			if r.JobCallback != nil {
				r.JobCallback(jobID, result.Status, jobStartedAt, jobFinishedAt)
			}
			if result.Status == JobStatusFailure {
				exitCode = 1
				failedJobs[jobID] = true
			}
		}
	}

	return exitCode, nil
}

// newRunID generates a random hex run ID.
func newRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// isExitError checks whether err is an *exec.ExitError and populates dst.
func isExitError(err error, dst **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*dst = e
	}
	return ok
}

// writeSecretsFile writes KEY=VALUE lines to a temp file with mode 0600.
// Returns the path to the file.
func writeSecretsFile(secrets map[string]string) (string, error) {
	f, err := os.CreateTemp("", "ghenkins-secrets-*")
	if err != nil {
		return "", fmt.Errorf("create secrets file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("chmod secrets file: %w", err)
	}
	for k, v := range secrets {
		if _, err := fmt.Fprintf(f, "%s=%s\n", k, v); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", fmt.Errorf("write secrets file: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("close secrets file: %w", err)
	}
	return f.Name(), nil
}

// secureDelete overwrites the file with zeros then removes it.
func secureDelete(path string) {
	if path == "" {
		return
	}
	if info, err := os.Stat(path); err == nil {
		if f, err := os.OpenFile(path, os.O_WRONLY, 0o600); err == nil {
			zeros := make([]byte, info.Size())
			_, _ = f.Write(zeros)
			_ = f.Sync()
			f.Close()
		}
	}
	_ = os.Remove(path)
}
