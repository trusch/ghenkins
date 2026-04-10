package runner

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateEventJSON_Push(t *testing.T) {
	data, err := GenerateEventJSON("owner/repo", "owner", "repo", "abc123", "main", 0, "push")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, "abc123", got["after"])
	assert.Equal(t, "refs/heads/main", got["ref"])

	repo, ok := got["repository"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "owner/repo", repo["full_name"])
	assert.Equal(t, "repo", repo["name"])

	owner, ok := repo["owner"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "owner", owner["login"])

	headCommit, ok := got["head_commit"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "abc123", headCommit["id"])
}

func TestGenerateEventJSON_PullRequest(t *testing.T) {
	data, err := GenerateEventJSON("owner/repo", "owner", "repo", "def456", "", 42, "pull_request")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, "synchronize", got["action"])
	assert.InDelta(t, 42, got["number"], 0.001)

	pr, ok := got["pull_request"].(map[string]any)
	require.True(t, ok)
	head, ok := pr["head"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "def456", head["sha"])
	assert.InDelta(t, 42, pr["number"], 0.001)

	repo, ok := got["repository"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "owner/repo", repo["full_name"])
	assert.Equal(t, "repo", repo["name"])

	owner, ok := repo["owner"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "owner", owner["login"])
}

func TestWriteSecretsFile(t *testing.T) {
	secrets := map[string]string{
		"TOKEN":    "secret-value",
		"API_KEY":  "key123",
	}

	path, err := writeSecretsFile(secrets)
	require.NoError(t, err)
	defer secureDelete(path)

	// Check file exists and has correct permissions
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Check file content contains expected KEY=VALUE lines
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	contentStr := string(content)
	assert.Contains(t, contentStr, "TOKEN=secret-value\n")
	assert.Contains(t, contentStr, "API_KEY=key123\n")
}

func TestWriteSecretsFile_Empty(t *testing.T) {
	path, err := writeSecretsFile(map[string]string{})
	require.NoError(t, err)
	defer secureDelete(path)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, content)
}

func TestSecureDelete_RemovesFile(t *testing.T) {
	f, err := os.CreateTemp("", "ghenkins-securedel-test-*")
	require.NoError(t, err)
	_, err = f.WriteString("sensitive data")
	require.NoError(t, err)
	f.Close()

	path := f.Name()
	secureDelete(path)

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "file should be deleted")
}

func TestSecureDelete_Noop(t *testing.T) {
	// Should not panic on empty path or nonexistent path
	secureDelete("")
	secureDelete("/tmp/ghenkins-nonexistent-file-xyz")
}
