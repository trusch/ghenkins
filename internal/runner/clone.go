package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ensureBareClone fetches if a bare clone exists at cacheDir/owner/repo.git,
// otherwise does `git clone --bare https://github.com/{owner}/{repo} {path}`.
// Returns the path to the bare clone.
func ensureBareClone(ctx context.Context, cacheDir, owner, repo string) (string, error) {
	bareDir := filepath.Join(cacheDir, owner, repo+".git")
	if _, err := os.Stat(bareDir); err == nil {
		// Already exists — fetch to update
		cmd := exec.CommandContext(ctx, "git", "fetch", "--all")
		cmd.Dir = bareDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git fetch in %s: %w\n%s", bareDir, err, out)
		}
		return bareDir, nil
	}

	if err := os.MkdirAll(filepath.Dir(bareDir), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(bareDir), err)
	}

	url := fmt.Sprintf("https://github.com/%s/%s", owner, repo)
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", url, bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone --bare %s: %w\n%s", url, err, out)
	}
	return bareDir, nil
}

// addWorktree creates `git worktree add <tmpdir> <sha>` in the bare clone.
// Returns (worktreePath, cleanupFunc, error).
// cleanup removes the worktree dir and runs `git worktree remove`.
func addWorktree(ctx context.Context, bareDir, sha string) (string, func(), error) {
	wtDir, err := os.MkdirTemp("", "ghenkins-wt-*")
	if err != nil {
		return "", nil, fmt.Errorf("mkdtemp: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", wtDir, sha)
	cmd.Dir = bareDir
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(wtDir)
		return "", nil, fmt.Errorf("git worktree add %s: %w\n%s", sha, err, out)
	}

	cleanup := func() {
		rmCmd := exec.Command("git", "worktree", "remove", "--force", wtDir)
		rmCmd.Dir = bareDir
		_ = rmCmd.Run()
		_ = os.RemoveAll(wtDir)
	}
	return wtDir, cleanup, nil
}
