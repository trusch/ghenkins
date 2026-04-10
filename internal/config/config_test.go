package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trusch/ghenkins/internal/config"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config*.yaml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		env     map[string]string
		check   func(t *testing.T, cfg *config.Config)
		wantErr string
	}{
		{
			name: "valid config with branch",
			yaml: `
github:
  token: "ghp_test"
  poll_interval: 60s
watches:
  - name: my-watch
    repo: owner/repo
    branch: main
    on: [push]
`,
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, "ghp_test", cfg.GitHub.Token)
				assert.Equal(t, 60*time.Second, cfg.GitHub.PollInterval)
				assert.Len(t, cfg.Watches, 1)
				assert.Equal(t, "my-watch", cfg.Watches[0].Name)
				assert.Equal(t, "owner/repo", cfg.Watches[0].Repo)
				assert.Equal(t, "main", cfg.Watches[0].Branch)
			},
		},
		{
			name: "valid config with pr",
			yaml: `
watches:
  - name: pr-watch
    repo: org/project
    pr: 42
    on: [pull_request]
`,
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, 42, cfg.Watches[0].PR)
			},
		},
		{
			name: "defaults applied",
			yaml: `
watches:
  - name: w
    repo: a/b
    branch: main
    on: [push]
`,
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, 2, cfg.MaxConcurrency)
				assert.Equal(t, 30*time.Second, cfg.GitHub.PollInterval)
				assert.Equal(t, "127.0.0.1:8765", cfg.LogServer.Bind)
				assert.Equal(t, 7, cfg.LogServer.RetentionDays)
				assert.Equal(t, int64(524288000), cfg.LogServer.RetentionBytes)
				assert.Contains(t, cfg.Store.Path, filepath.Join(".local", "share", "ghenkins"))
			},
		},
		{
			name: "envsubst expansion",
			yaml: `
github:
  token: "${TEST_GH_TOKEN}"
watches:
  - name: w
    repo: a/b
    branch: main
    on: [push]
`,
			env: map[string]string{"TEST_GH_TOKEN": "expanded-token"},
			check: func(t *testing.T, cfg *config.Config) {
				assert.Equal(t, "expanded-token", cfg.GitHub.Token)
			},
		},
		{
			name: "missing pr and branch",
			yaml: `
watches:
  - name: bad
    repo: owner/repo
    on: [push]
`,
			wantErr: "must set either pr or branch",
		},
		{
			name: "both pr and branch",
			yaml: `
watches:
  - name: bad
    repo: owner/repo
    pr: 1
    branch: main
    on: [push]
`,
			wantErr: "cannot set both pr and branch",
		},
		{
			name: "invalid repo format",
			yaml: `
watches:
  - name: bad
    repo: notaslashrepo
    branch: main
    on: [push]
`,
			wantErr: `repo must be in owner/repo form`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path := writeTemp(t, tc.yaml)
			cfg, err := config.Load(path)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			tc.check(t, cfg)
		})
	}
}
