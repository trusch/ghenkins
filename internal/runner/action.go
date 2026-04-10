package runner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ActionType classifies a resolved uses: reference.
type ActionType int

const (
	ActionTypeComposite  ActionType = iota // owner/repo@ref with composite runs.using
	ActionTypeJS                           // node20/node24 — stubbed
	ActionTypeDocker                       // docker:// image reference
	ActionTypeDockerfile                   // docker action with Dockerfile — stubbed
	ActionTypeLocal                        // ./local/path composite action
)

// ResolvedAction is the result of resolving a uses: string.
type ResolvedAction struct {
	Type      ActionType
	Def       *ActionDef // parsed action.yml (nil for raw docker://)
	SourceDir string     // host path to action source (composite + local)
	Image     string     // docker image name (docker actions)
	Uses      string     // original uses: string
}

// githubRawBaseURL is the base URL for fetching raw files from GitHub.
// Overridable in tests.
var githubRawBaseURL = "https://raw.githubusercontent.com"

// ResolveAction parses a uses: string and returns the resolved action.
// workspacePath is the host path of /workspace (for ./local paths).
// cacheDir is ~/.cache/ghenkins/actions.
func ResolveAction(ctx context.Context, uses, workspacePath, cacheDir string) (*ResolvedAction, error) {
	switch {
	case strings.HasPrefix(uses, "docker://"):
		image := strings.TrimPrefix(uses, "docker://")
		return &ResolvedAction{Type: ActionTypeDocker, Image: image, Uses: uses}, nil
	case strings.HasPrefix(uses, "./"):
		return resolveLocalAction(uses, workspacePath)
	default:
		return resolveRemoteAction(ctx, uses, cacheDir)
	}
}

// resolveLocalAction resolves a ./local/path uses: reference.
func resolveLocalAction(uses, workspacePath string) (*ResolvedAction, error) {
	absPath := filepath.Join(workspacePath, uses[2:])
	data, err := readActionYAML(absPath)
	if err != nil {
		return nil, fmt.Errorf("resolve local action %q: %w", uses, err)
	}
	def, err := ParseActionDef(data)
	if err != nil {
		return nil, fmt.Errorf("parse action def for %q: %w", uses, err)
	}
	ra := &ResolvedAction{Def: def, SourceDir: absPath, Uses: uses}
	switch {
	case def.Runs.Using == "composite":
		ra.Type = ActionTypeLocal
	case isJSUsing(def.Runs.Using):
		ra.Type = ActionTypeJS
	case def.Runs.Using == "docker":
		ra.Type = ActionTypeDockerfile
	default:
		ra.Type = ActionTypeJS // unknown → stub
	}
	return ra, nil
}

// resolveRemoteAction resolves an owner/repo@ref uses: reference.
// It fetches action.yml from GitHub (or cache) and determines the action type.
// For composite actions, the repo is cloned lazily in ExecActionStep.
func resolveRemoteAction(ctx context.Context, uses, cacheDir string) (*ResolvedAction, error) {
	atIdx := strings.LastIndex(uses, "@")
	if atIdx < 0 {
		return nil, fmt.Errorf("invalid uses %q: missing @ref", uses)
	}
	ownerRepo, ref := uses[:atIdx], uses[atIdx+1:]
	slash := strings.Index(ownerRepo, "/")
	if slash < 0 {
		return nil, fmt.Errorf("invalid uses %q: missing owner/repo", uses)
	}
	owner, repo := ownerRepo[:slash], ownerRepo[slash+1:]

	cacheKey := filepath.Join(cacheDir, owner, repo, ref)
	cachedFile := filepath.Join(cacheKey, "action.yml")

	var data []byte
	if _, err := os.Stat(cachedFile); err == nil {
		// Cache hit — read from disk.
		data, err = os.ReadFile(cachedFile)
		if err != nil {
			return nil, fmt.Errorf("read cached action.yml for %s: %w", uses, err)
		}
	} else {
		// Fetch from GitHub raw content.
		var fetchErr error
		data, fetchErr = fetchRaw(ctx, owner, repo, ref, "action.yml")
		if fetchErr != nil {
			// Some repos use .yaml extension.
			data, fetchErr = fetchRaw(ctx, owner, repo, ref, "action.yaml")
			if fetchErr != nil {
				return nil, fmt.Errorf("fetch action def for %s: not found as action.yml or action.yaml", uses)
			}
		}
		// Cache the file.
		if err := os.MkdirAll(cacheKey, 0o755); err != nil {
			return nil, fmt.Errorf("create cache dir for %s: %w", uses, err)
		}
		if err := os.WriteFile(cachedFile, data, 0o644); err != nil {
			return nil, fmt.Errorf("cache action.yml for %s: %w", uses, err)
		}
	}

	def, err := ParseActionDef(data)
	if err != nil {
		return nil, fmt.Errorf("parse action def for %s: %w", uses, err)
	}

	ra := &ResolvedAction{Def: def, Uses: uses}
	switch {
	case def.Runs.Using == "composite":
		ra.Type = ActionTypeComposite
		// SourceDir points to the cache key; the repo is cloned lazily in ExecActionStep.
		ra.SourceDir = cacheKey
	case isJSUsing(def.Runs.Using):
		ra.Type = ActionTypeJS
	case def.Runs.Using == "docker":
		img := def.Runs.Image
		if img == "Dockerfile" || img == "" || !strings.HasPrefix(img, "docker://") {
			ra.Type = ActionTypeDockerfile
		} else {
			ra.Type = ActionTypeDocker
			ra.Image = strings.TrimPrefix(img, "docker://")
		}
	default:
		ra.Type = ActionTypeJS // unknown → stub as unsupported
	}

	return ra, nil
}

// fetchRaw downloads a single file from GitHub raw content.
func fetchRaw(ctx context.Context, owner, repo, ref, filename string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s/%s/%s/%s", githubRawBaseURL, owner, repo, ref, filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found: %s", url)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}

// readActionYAML tries action.yml then action.yaml in dir.
func readActionYAML(dir string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, "action.yml"))
	if err == nil {
		return data, nil
	}
	return os.ReadFile(filepath.Join(dir, "action.yaml"))
}

// isJSUsing returns true for Node.js action runner values.
func isJSUsing(using string) bool {
	switch using {
	case "node12", "node16", "node20", "node24":
		return true
	}
	return false
}

// needsClone returns true if the action dir has not been fully cloned yet.
// A dir with only action.yml (≤1 entry) is treated as not yet cloned.
func needsClone(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return false // already a git repo
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	return len(entries) <= 1
}

// cloneActionRepo clones owner/repo at ref into destDir using git clone --depth=1.
// If destDir already contains action.yml, the clone goes to a temp dir and is
// merged on top so the cached file is preserved.
func cloneActionRepo(ctx context.Context, owner, repo, ref, destDir string) error {
	repoURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)

	tmpDir, err := os.MkdirTemp("", "ghenkins-clone-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--branch", ref, repoURL, tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s@%s: %w (output: %s)", repoURL, ref, err, out)
	}

	return copyDir(tmpDir, destDir)
}

// sanitizeUses converts a uses: string into a filesystem-safe directory name.
func sanitizeUses(uses string) string {
	return strings.NewReplacer("/", "_", "@", "_", ".", "-", ":", "_").Replace(uses)
}

// copyDir recursively copies all files from src into dst.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, 0o644)
	})
}

// copyStringMap returns a shallow copy of m.
func copyStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ExecActionStep executes a uses: step inside the existing job container.
// For composite/local: runs the action's steps recursively in the same container.
// For docker://: spawns a new ephemeral container via the podman CLI.
// For JS/Dockerfile: returns an explicit unsupported error.
func (r *podmanJobRunner) ExecActionStep(
	ctx context.Context,
	step *Step,
	resolved *ResolvedAction,
	container *Container,
	execCtx *ExecutionContext,
	info JobInfo,
	envFiles *EnvFiles,
	conn context.Context, // podman connection (used for docker action pull)
	logWriter io.Writer,
) (StepResult, error) {
	switch resolved.Type {
	case ActionTypeComposite, ActionTypeLocal:
		return r.execCompositeAction(ctx, step, resolved, container, execCtx, info, envFiles, logWriter)
	case ActionTypeDocker:
		return r.execDockerAction(ctx, step, resolved, execCtx, logWriter)
	case ActionTypeJS, ActionTypeDockerfile:
		using := ""
		if resolved.Def != nil {
			using = resolved.Def.Runs.Using
		}
		return StepResult{Outcome: "failure"}, fmt.Errorf(
			"action %q uses %q which is not supported in ghenkins MVP; use a composite action or docker:// alternative",
			step.Uses, using,
		)
	default:
		return StepResult{Outcome: "failure"}, fmt.Errorf("unsupported action type for %q", step.Uses)
	}
}

// execCompositeAction runs a composite action's steps in the existing job container.
func (r *podmanJobRunner) execCompositeAction(
	ctx context.Context,
	step *Step,
	resolved *ResolvedAction,
	container *Container,
	execCtx *ExecutionContext,
	info JobInfo,
	envFiles *EnvFiles,
	logWriter io.Writer,
) (StepResult, error) {
	if resolved.Def == nil {
		return StepResult{Outcome: "failure"}, fmt.Errorf("composite action %q has no definition", step.Uses)
	}

	// Lazily clone the action repo if the source dir only has action.yml.
	if resolved.Type == ActionTypeComposite && resolved.SourceDir != "" && needsClone(resolved.SourceDir) {
		atIdx := strings.LastIndex(resolved.Uses, "@")
		if atIdx >= 0 {
			ownerRepo, ref := resolved.Uses[:atIdx], resolved.Uses[atIdx+1:]
			slash := strings.Index(ownerRepo, "/")
			if slash >= 0 {
				owner, repo := ownerRepo[:slash], ownerRepo[slash+1:]
				if err := cloneActionRepo(ctx, owner, repo, ref, resolved.SourceDir); err != nil {
					return StepResult{Outcome: "failure"}, fmt.Errorf("clone action %s: %w", resolved.Uses, err)
				}
			}
		}
	}

	// Interpolate step.With values using the parent context.
	evalCtx := buildStepEvalCtx(execCtx, step, &info)
	eval := execCtx.Eval
	if eval == nil {
		eval = &Evaluator{}
	}

	inputs := make(map[string]string, len(step.With))
	for k, v := range step.With {
		inputs[k] = eval.Interpolate(v, evalCtx)
	}
	// Fill defaults from action definition.
	for name, inp := range resolved.Def.Inputs {
		if _, ok := inputs[name]; !ok && inp.Default != "" {
			inputs[name] = inp.Default
		}
	}

	// Create a nested ExecutionContext for the composite action's steps.
	nested := &ExecutionContext{
		Eval:           &Evaluator{},
		StepResults:    make(map[string]StepResult),
		LiveEnv:        copyStringMap(execCtx.LiveEnv),
		LivePath:       append([]string(nil), execCtx.LivePath...),
		JobStatus:      JobStatusPending,
		Cancelled:      execCtx.Cancelled,
		Secrets:        execCtx.Secrets,
		Inputs:         inputs,
		DefaultShell:   execCtx.DefaultShell,
		DefaultWorkdir: execCtx.DefaultWorkdir,
	}

	// Execute each composite step sequentially.
	for _, compStep := range resolved.Def.Runs.Steps {
		result, err := r.RunStep(ctx, compStep, container, nested, info, envFiles, logWriter)
		if compStep.ID != "" {
			nested.StepResults[compStep.ID] = result
		}
		if err != nil || result.Outcome == "failure" {
			if !compStep.ContinueOnError {
				nested.JobStatus = JobStatusFailure
				break
			}
		}
		if result.Outcome == "success" && nested.JobStatus == JobStatusPending {
			nested.JobStatus = JobStatusPending // keep pending until all steps done
		}
	}

	// Map composite action outputs.
	outputs := make(map[string]string)
	if len(resolved.Def.Outputs) > 0 {
		nestedEvalCtx := buildStepEvalCtx(nested, step, &info)
		// Override inputs context so output expressions like ${{ steps.foo.outputs.bar }} resolve.
		nestedEvalCtx.Steps = nested.StepResults
		nestedEvalCtx.Inputs = inputs
		for name, outDef := range resolved.Def.Outputs {
			if outDef.Value != "" {
				outputs[name] = eval.Interpolate(outDef.Value, nestedEvalCtx)
			}
		}
	}

	outcome := "success"
	conclusion := "success"
	if nested.JobStatus == JobStatusFailure {
		outcome = "failure"
		conclusion = "failure"
	}
	return StepResult{Outcome: outcome, Conclusion: conclusion, Outputs: outputs}, nil
}

// execDockerAction spawns an ephemeral container for a docker:// action.
func (r *podmanJobRunner) execDockerAction(
	ctx context.Context,
	step *Step,
	resolved *ResolvedAction,
	execCtx *ExecutionContext,
	logWriter io.Writer,
) (StepResult, error) {
	args := []string{"run", "--rm"}

	// Pass live environment.
	for k, v := range execCtx.LiveEnv {
		args = append(args, "-e", k+"="+v)
	}
	// Pass step.With as INPUT_<UPPER_KEY>=value per the Actions spec.
	for k, v := range step.With {
		inputKey := "INPUT_" + strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
		args = append(args, "-e", inputKey+"="+v)
	}

	// Mount workspace at /github/workspace (docker action convention).
	if r.WorkspaceDir != "" {
		args = append(args, "-v", r.WorkspaceDir+":/github/workspace:rw")
		args = append(args, "-w", "/github/workspace")
	}

	args = append(args, resolved.Image)

	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if isExitError(err, &exitErr) {
			if exitErr.ExitCode() != 0 {
				return StepResult{Outcome: "failure", Conclusion: "failure"}, nil
			}
		}
		return StepResult{Outcome: "failure"}, fmt.Errorf("run docker action %q: %w", resolved.Image, err)
	}
	return StepResult{Outcome: "success", Conclusion: "success"}, nil
}
