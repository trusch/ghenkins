package runner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trusch/ghenkins/internal/store"
)

// JobInfo holds the per-run metadata needed to populate GITHUB_* env vars.
type JobInfo struct {
	SHA          string
	Ref          string   // e.g. "refs/heads/main"
	RefName      string   // e.g. "main"
	EventName    string   // "push" or "pull_request"
	Repo         string   // "owner/repo"
	Owner        string
	RepoName     string
	RunID        string
	RunNumber    int
	Actor        string // default "ghenkins"
	EventPayload []byte // raw event JSON
	PRNumber     int    // 0 if not a PR
	HeadRef      string // PR head branch (empty if push)
	BaseRef      string // PR base branch (empty if push)
}

// JobResult is the outcome of running a single job.
type JobResult struct {
	JobID  string
	Status JobStatus
	Steps  map[string]StepResult
}

// ExecutionContext is shared state mutated as a job progresses.
type ExecutionContext struct {
	Eval           *Evaluator
	StepResults    map[string]StepResult
	LiveEnv        map[string]string // accumulated env (workflow + job + GITHUB_ENV writes)
	LivePath       []string          // accumulated extra PATH entries
	JobStatus      JobStatus
	Cancelled      bool
	Secrets        map[string]string
	Inputs         map[string]string // for composite action inputs
	DefaultShell   string            // resolved from job/workflow defaults
	DefaultWorkdir string            // resolved from job/workflow defaults
}

// podmanJobRunner runs individual jobs in Podman containers.
type podmanJobRunner struct {
	conn         context.Context // Podman bindings connection context
	WorkspaceDir string          // host path mounted at /workspace in containers
	cacheDir     string          // host path for caching action repos (~/.cache/ghenkins)
	store        store.Store     // nil if not wired; used to persist artifacts
}

// newPodmanJobRunner creates a podmanJobRunner connected to the Podman socket.
func newPodmanJobRunner(ctx context.Context, workspaceDir, cacheDir string) (*podmanJobRunner, error) {
	conn, err := Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to podman: %w", err)
	}
	return &podmanJobRunner{conn: conn, WorkspaceDir: workspaceDir, cacheDir: cacheDir}, nil
}

// RunJob executes all steps of a single job in a Podman container.
func (r *podmanJobRunner) RunJob(ctx context.Context, jobID string, job *Job, wf *Workflow, info JobInfo, secrets map[string]string, logWriter io.Writer, defaultImage string) (JobResult, error) {
	eval := &Evaluator{}

	if secrets == nil {
		secrets = make(map[string]string)
	}

	execCtx := &ExecutionContext{
		Eval:        eval,
		StepResults: make(map[string]StepResult),
		LiveEnv:     make(map[string]string),
		LivePath:    nil,
		JobStatus:   JobStatusPending,
		Cancelled:   false,
		Secrets:     secrets,
		Inputs:      make(map[string]string),
	}

	// Resolve shell/workdir defaults from job then workflow
	if job.Defaults != nil {
		execCtx.DefaultShell = job.Defaults.Run.Shell
		execCtx.DefaultWorkdir = job.Defaults.Run.WorkingDirectory
	}
	if execCtx.DefaultShell == "" && wf.Defaults != nil {
		execCtx.DefaultShell = wf.Defaults.Run.Shell
	}
	if execCtx.DefaultWorkdir == "" && wf.Defaults != nil {
		execCtx.DefaultWorkdir = wf.Defaults.Run.WorkingDirectory
	}

	// 1. Evaluate job if: condition — skip all steps if false
	if job.If != "" {
		evalCtx := BuildEvalContext(wf, jobID, job, nil, execCtx.StepResults, execCtx.LiveEnv, &info, secrets, execCtx.JobStatus, execCtx.Cancelled)
		ok, err := eval.EvalBool(job.If, evalCtx)
		if err != nil {
			return JobResult{JobID: jobID, Status: JobStatusFailure, Steps: execCtx.StepResults},
				fmt.Errorf("evaluate job if condition: %w", err)
		}
		if !ok {
			return JobResult{JobID: jobID, Status: JobStatusSuccess, Steps: execCtx.StepResults}, nil
		}
	}

	// 2. Apply timeout; default 360 minutes
	timeoutMin := job.TimeoutMinutes
	if timeoutMin <= 0 {
		timeoutMin = 360
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMin)*time.Minute)
	defer cancel()

	// 3. Resolve container image
	image := defaultImage
	if image == "" {
		image = job.RunsOnImage()
	}

	// 4. Create OS temp dir for runner files
	runnerDir, err := os.MkdirTemp("", "ghenkins-runner-*")
	if err != nil {
		return JobResult{JobID: jobID, Status: JobStatusFailure, Steps: execCtx.StepResults},
			fmt.Errorf("create runner dir: %w", err)
	}
	defer os.RemoveAll(runnerDir)

	// 5. Create EnvFiles in that dir
	envFiles, err := NewEnvFiles(runnerDir)
	if err != nil {
		return JobResult{JobID: jobID, Status: JobStatusFailure, Steps: execCtx.StepResults},
			fmt.Errorf("create env files: %w", err)
	}

	// 6. Write event JSON
	eventPayload := info.EventPayload
	if len(eventPayload) == 0 {
		eventPayload = []byte("{}")
	}
	if err := os.WriteFile(runnerDir+"/_event.json", eventPayload, 0644); err != nil {
		return JobResult{JobID: jobID, Status: JobStatusFailure, Steps: execCtx.StepResults},
			fmt.Errorf("write event json: %w", err)
	}

	// 7. Build initial env map
	initialEnv := buildInitialEnv(wf, jobID, job, &info)
	execCtx.LiveEnv = initialEnv

	// 8. Pull image
	if err := PullImage(r.conn, image, logWriter); err != nil {
		return JobResult{JobID: jobID, Status: JobStatusFailure, Steps: execCtx.StepResults},
			fmt.Errorf("pull image %s: %w", image, err)
	}

	// 9. Pre-scan steps for composite actions so their source dirs are bind-mounted.
	actionCacheDir := filepath.Join(r.cacheDir, "actions")
	extraBinds := r.resolveExtraBinds(ctx, job.Steps, execCtx, info, actionCacheDir)

	// Append persistent Go module + build caches so downloads survive across runs.
	goModCache := filepath.Join(r.cacheDir, "go", "pkg", "mod")
	goBuildCache := filepath.Join(r.cacheDir, "go", "build-cache")
	_ = os.MkdirAll(goModCache, 0o755)
	_ = os.MkdirAll(goBuildCache, 0o755)
	extraBinds = append(extraBinds,
		BindMount{HostPath: goModCache, ContainerPath: "/root/go/pkg/mod"},
		BindMount{HostPath: goBuildCache, ContainerPath: "/root/.cache/go/build"},
	)

	// Persist apt package archives and lists so apt-get install is faster on
	// subsequent runs (packages are already downloaded; only dpkg runs again).
	aptArchives := filepath.Join(r.cacheDir, "apt", "archives")
	aptLists := filepath.Join(r.cacheDir, "apt", "lists")
	_ = os.MkdirAll(aptArchives, 0o755)
	_ = os.MkdirAll(aptLists, 0o755)
	extraBinds = append(extraBinds,
		BindMount{HostPath: aptArchives, ContainerPath: "/var/cache/apt/archives"},
		BindMount{HostPath: aptLists, ContainerPath: "/var/lib/apt/lists"},
	)

	// Per-run artifact dir: steps write to /artifacts/, we collect after run
	artifactDir := filepath.Join(r.cacheDir, "artifacts", info.RunID)
	_ = os.MkdirAll(artifactDir, 0o755)
	extraBinds = append(extraBinds,
		BindMount{HostPath: artifactDir, ContainerPath: "/artifacts"},
	)

	// 10. Create + start container
	containerName := fmt.Sprintf("ghenkins-%s-%d", sanitizeName(jobID), time.Now().UnixNano())
	container, err := CreateContainer(r.conn, ContainerConfig{
		Image:         image,
		Name:          containerName,
		Env:           initialEnv,
		WorkspaceHost: r.WorkspaceDir,
		RunnerDirHost: runnerDir,
		ExtraBinds:    extraBinds,
	})
	if err != nil {
		return JobResult{JobID: jobID, Status: JobStatusFailure, Steps: execCtx.StepResults},
			fmt.Errorf("create container: %w", err)
	}
	defer container.Remove(context.Background()) //nolint:errcheck

	if err := container.Start(ctx); err != nil {
		return JobResult{JobID: jobID, Status: JobStatusFailure, Steps: execCtx.StepResults},
			fmt.Errorf("start container: %w", err)
	}

	// 12. For each step in job.Steps
	for i, step := range job.Steps {
		stepID := step.ID
		if stepID == "" {
			stepID = fmt.Sprintf("__step_%d", i)
			step.ID = stepID
		}

		evalCtx := BuildEvalContext(wf, jobID, job, step, execCtx.StepResults, execCtx.LiveEnv, &info, secrets, execCtx.JobStatus, execCtx.Cancelled)

		// Evaluate step if: condition
		shouldRun := true
		if step.If != "" {
			ok, err := eval.EvalBool(step.If, evalCtx)
			if err != nil {
				shouldRun = false
			} else {
				shouldRun = ok
			}
		} else {
			// Implicit success() — run only if job has not failed
			shouldRun = execCtx.JobStatus == JobStatusSuccess || execCtx.JobStatus == JobStatusPending
		}

		if !shouldRun {
			execCtx.StepResults[stepID] = StepResult{Outcome: "skipped", Conclusion: "skipped"}
			continue
		}

		result, stepErr := r.RunStep(ctx, step, container, execCtx, info, envFiles, logWriter)
		if stepErr != nil {
			result = StepResult{Outcome: "failure", Conclusion: "failure"}
		}

		// Read delta from env files and propagate into execCtx
		newEnv, newOutputs, newPaths, _ := envFiles.ReadDelta()
		for k, v := range newEnv {
			execCtx.LiveEnv[k] = v
		}
		execCtx.LivePath = append(execCtx.LivePath, newPaths...)

		// Merge step outputs into result
		if len(newOutputs) > 0 {
			if result.Outputs == nil {
				result.Outputs = make(map[string]string)
			}
			for k, v := range newOutputs {
				result.Outputs[k] = v
			}
		}

		execCtx.StepResults[stepID] = result

		// On failure, mark job failed — but do NOT break; remaining steps may have if: always()
		if result.Outcome == "failure" && !step.ContinueOnError {
			execCtx.JobStatus = JobStatusFailure
		}
	}

	// Finalize status: pending with no failures → success
	finalStatus := execCtx.JobStatus
	if finalStatus == JobStatusPending {
		finalStatus = JobStatusSuccess
	}

	// Collect artifacts written to /artifacts/ inside the container
	if r.store != nil {
		_ = filepath.WalkDir(artifactDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			h := sha256.New()
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			_, _ = io.Copy(h, f)
			_ = r.store.UpsertArtifact(context.Background(), store.RunArtifact{
				ID:       uuid.New().String(),
				RunID:    info.RunID,
				Filename: filepath.Base(path),
				Size:     fi.Size(),
				SHA256:   fmt.Sprintf("%x", h.Sum(nil)),
			})
			return nil
		})
	}

	return JobResult{
		JobID:  jobID,
		Status: finalStatus,
		Steps:  execCtx.StepResults,
	}, nil
}

// RunStep executes a single step. Dispatches to runShellStep or runUsesStep.
func (r *podmanJobRunner) RunStep(ctx context.Context, step *Step, container *Container, execCtx *ExecutionContext, info JobInfo, envFiles *EnvFiles, logWriter io.Writer) (StepResult, error) {
	// Build eval context for interpolation within this step
	evalCtx := buildStepEvalCtx(execCtx, step, &info)

	if step.Run != "" {
		return r.runShellStep(ctx, step, container, execCtx, evalCtx, logWriter)
	}
	if step.Uses != "" {
		return r.runUsesStep(ctx, step, container, execCtx, info, envFiles, logWriter)
	}
	return StepResult{Outcome: "success", Conclusion: "success"}, nil
}

// runShellStep executes a run: step inside the container.
func (r *podmanJobRunner) runShellStep(ctx context.Context, step *Step, container *Container, execCtx *ExecutionContext, evalCtx *EvalContext, logWriter io.Writer) (StepResult, error) {
	eval := execCtx.Eval

	// Determine shell: step → job default → workflow default → bash
	shell := step.Shell
	if shell == "" {
		shell = execCtx.DefaultShell
	}
	if shell == "" {
		shell = "bash"
	}

	// Interpolate the script
	script := eval.Interpolate(step.Run, evalCtx)

	// Build command
	cmd := buildShellCmd(shell, script)

	// Build env slice: LiveEnv + step env (interpolated)
	envMap := make(map[string]string, len(execCtx.LiveEnv)+len(step.Env))
	for k, v := range execCtx.LiveEnv {
		envMap[k] = v
	}
	for k, v := range step.Env {
		envMap[k] = eval.Interpolate(v, evalCtx)
	}

	// Prepend LivePath entries to PATH
	if len(execCtx.LivePath) > 0 {
		base := envMap["PATH"]
		if base == "" {
			base = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
		}
		envMap["PATH"] = strings.Join(execCtx.LivePath, ":") + ":" + base
	}

	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}

	// Determine working directory
	workdir := step.WorkingDirectory
	if workdir == "" {
		workdir = execCtx.DefaultWorkdir
	}
	if workdir != "" {
		workdir = eval.Interpolate(workdir, evalCtx)
	}
	if workdir == "" {
		workdir = "/workspace"
	}

	exitCode, err := container.Exec(ctx, cmd, envSlice, workdir, logWriter, logWriter)
	if err != nil {
		return StepResult{Outcome: "failure", Conclusion: "failure"}, err
	}
	if exitCode != 0 {
		return StepResult{Outcome: "failure", Conclusion: "failure"}, nil
	}
	return StepResult{Outcome: "success", Conclusion: "success"}, nil
}

// runUsesStep handles uses: steps by resolving the action and dispatching to ExecActionStep.
func (r *podmanJobRunner) runUsesStep(ctx context.Context, step *Step, container *Container, execCtx *ExecutionContext, info JobInfo, envFiles *EnvFiles, logWriter io.Writer) (StepResult, error) {
	eval := execCtx.Eval
	evalCtx := buildStepEvalCtx(execCtx, step, &info)

	uses := eval.Interpolate(step.Uses, evalCtx)

	// Special case: actions/checkout — ghenkins already clones the repo into /workspace
	// before the container starts, so treat this as a no-op success.
	if isCheckoutAction(uses) {
		fmt.Fprintf(logWriter, "## ghenkins: skipping %s (repo already checked out at /workspace)\n", uses)
		return StepResult{Outcome: "success", Conclusion: "success"}, nil
	}

	actionCacheDir := filepath.Join(r.cacheDir, "actions")
	resolved, err := ResolveAction(ctx, uses, r.WorkspaceDir, actionCacheDir)
	if err != nil {
		return StepResult{Outcome: "failure", Conclusion: "failure"}, fmt.Errorf("resolve action %q: %w", uses, err)
	}

	return r.ExecActionStep(ctx, step, resolved, container, execCtx, info, envFiles, r.conn, logWriter)
}

// isCheckoutAction returns true for any variant of actions/checkout.
func isCheckoutAction(uses string) bool {
	return strings.HasPrefix(strings.ToLower(uses), "actions/checkout@")
}

// resolveExtraBinds pre-scans steps for composite actions and returns bind mounts
// to include in the container config so that action source dirs are accessible
// inside the container at /actions/<sanitized-uses>.
// Errors are non-fatal; unresolvable actions will surface again at execution time.
func (r *podmanJobRunner) resolveExtraBinds(ctx context.Context, steps []*Step, execCtx *ExecutionContext, info JobInfo, actionCacheDir string) []BindMount {
	var binds []BindMount
	seen := make(map[string]bool)
	eval := execCtx.Eval
	evalCtx := buildStepEvalCtx(execCtx, &Step{}, &info)

	for _, step := range steps {
		if step.Uses == "" {
			continue
		}
		uses := eval.Interpolate(step.Uses, evalCtx)
		if isCheckoutAction(uses) || strings.HasPrefix(uses, "docker://") || strings.HasPrefix(uses, "./") {
			continue
		}
		resolved, err := ResolveAction(ctx, uses, r.WorkspaceDir, actionCacheDir)
		if err != nil {
			continue
		}
		if resolved.Type != ActionTypeComposite || resolved.SourceDir == "" {
			continue
		}
		if seen[resolved.SourceDir] {
			continue
		}
		seen[resolved.SourceDir] = true

		// Eagerly clone the action repo so the bind-mount source exists before CreateContainer.
		if needsClone(resolved.SourceDir) {
			atIdx := strings.LastIndex(resolved.Uses, "@")
			if atIdx >= 0 {
				ownerRepo, ref := resolved.Uses[:atIdx], resolved.Uses[atIdx+1:]
				slash := strings.Index(ownerRepo, "/")
				if slash >= 0 {
					owner, repo := ownerRepo[:slash], ownerRepo[slash+1:]
					if err := cloneActionRepo(ctx, owner, repo, ref, resolved.SourceDir); err != nil {
						continue // will fail again at execution time
					}
				}
			}
		}

		containerPath := "/actions/" + sanitizeUses(uses)
		binds = append(binds, BindMount{
			HostPath:      resolved.SourceDir,
			ContainerPath: containerPath,
			ReadOnly:      true,
		})
	}
	return binds
}

// buildShellCmd constructs the shell command slice for a run: step.
func buildShellCmd(shell, script string) []string {
	switch shell {
	case "bash", "":
		return []string{"/bin/bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script}
	case "sh":
		return []string{"/bin/sh", "-e", "-c", script}
	default:
		return []string{shell, "-c", script}
	}
}

// buildInitialEnv builds the initial environment map for a job container,
// merging workflow env, job env, and all standard GITHUB_* / RUNNER_* vars.
func buildInitialEnv(wf *Workflow, jobID string, job *Job, info *JobInfo) map[string]string {
	env := make(map[string]string)

	for k, v := range wf.Env {
		env[k] = v
	}
	for k, v := range job.Env {
		env[k] = v
	}

	actor := info.Actor
	if actor == "" {
		actor = "ghenkins"
	}

	env["CI"] = "true"
	env["GITHUB_ACTIONS"] = "true"
	env["GITHUB_SHA"] = info.SHA
	env["GITHUB_REF"] = info.Ref
	env["GITHUB_REF_NAME"] = info.RefName
	env["GITHUB_REF_TYPE"] = "branch"
	env["GITHUB_REPOSITORY"] = info.Repo
	env["GITHUB_REPOSITORY_OWNER"] = info.Owner
	env["GITHUB_EVENT_NAME"] = info.EventName
	env["GITHUB_EVENT_PATH"] = "/runner/_event.json"
	env["GITHUB_WORKSPACE"] = "/workspace"
	env["GITHUB_ENV"] = "/runner/_env"
	env["GITHUB_OUTPUT"] = "/runner/_output"
	env["GITHUB_PATH"] = "/runner/_path"
	env["GITHUB_STEP_SUMMARY"] = "/runner/_summary"
	env["GITHUB_RUN_ID"] = info.RunID
	env["GITHUB_RUN_NUMBER"] = fmt.Sprintf("%d", info.RunNumber)
	env["GITHUB_SERVER_URL"] = "https://github.com"
	env["GITHUB_API_URL"] = "https://api.github.com"
	env["GITHUB_GRAPHQL_URL"] = "https://api.github.com/graphql"
	env["GITHUB_ACTOR"] = actor
	env["GITHUB_JOB"] = jobID
	env["GITHUB_WORKFLOW"] = wf.Name
	env["RUNNER_OS"] = "Linux"
	env["RUNNER_ARCH"] = "X64"
	env["RUNNER_NAME"] = "ghenkins"
	env["RUNNER_TEMP"] = "/runner/_temp"
	env["RUNNER_TOOL_CACHE"] = "/runner/_tools"

	if info.EventName == "pull_request" {
		env["GITHUB_HEAD_REF"] = info.HeadRef
		env["GITHUB_BASE_REF"] = info.BaseRef
	}

	return env
}

// buildStepEvalCtx builds an EvalContext for interpolation within a single step.
func buildStepEvalCtx(execCtx *ExecutionContext, step *Step, info *JobInfo) *EvalContext {
	githubCtx := map[string]interface{}{
		"sha":              info.SHA,
		"ref":              info.Ref,
		"ref_name":         info.RefName,
		"event_name":       info.EventName,
		"repository":       info.Repo,
		"repository_owner": info.Owner,
		"actor":            info.Actor,
		"run_id":           info.RunID,
		"run_number":       info.RunNumber,
		"workspace":        "/workspace",
	}

	mergedEnv := make(map[string]string, len(execCtx.LiveEnv)+len(step.Env))
	for k, v := range execCtx.LiveEnv {
		mergedEnv[k] = v
	}
	for k, v := range step.Env {
		mergedEnv[k] = v
	}

	return &EvalContext{
		Github: githubCtx,
		Env:    mergedEnv,
		Job: map[string]interface{}{
			"status": jobStatusString(execCtx.JobStatus),
		},
		Steps: execCtx.StepResults,
		Runner: map[string]interface{}{
			"os":         "Linux",
			"arch":       "X64",
			"temp":       "/runner/_temp",
			"tool_cache": "/runner/_tools",
			"name":       "ghenkins",
		},
		Secrets:   execCtx.Secrets,
		Inputs:    execCtx.Inputs,
		JobStatus: execCtx.JobStatus,
		Cancelled: execCtx.Cancelled,
	}
}

// sanitizeName replaces characters invalid in Podman container names.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
