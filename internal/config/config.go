package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drone/envsubst"
	"gopkg.in/yaml.v3"
)

type Config struct {
	GitHub         GitHubConfig    `yaml:"github"`
	LogServer      LogServerConfig `yaml:"log_server"`
	Store          StoreConfig     `yaml:"store"`
	Runner         RunnerConfig    `yaml:"runner"`
	MaxConcurrency int             `yaml:"max_concurrency"` // default: 2
	Projects       []Project       `yaml:"projects"`        // new
	Watches        []Watch         `yaml:"watches"`         // deprecated, still parsed for compat
}

type RunnerConfig struct {
	DefaultImage string `yaml:"default_image"`  // default: "ubuntu:22.04"
	WorkspaceDir string `yaml:"workspace_dir"`  // host path used as root for all temp mounts; default: ~/.cache/ghenkins/workspace
}

type GitHubConfig struct {
	Token        string        `yaml:"token"`
	PollInterval time.Duration `yaml:"poll_interval"` // default: 30s
}

type LogServerConfig struct {
	Bind           string        `yaml:"bind"`            // default: "127.0.0.1:8765"
	RetentionDays  int           `yaml:"retention_days"`  // default: 7
	RetentionBytes int64         `yaml:"retention_bytes"` // default: 524288000 (500MB)
}

type StoreConfig struct {
	Path string `yaml:"path"` // default: ~/.local/share/ghenkins/ghenkins.db
}

// Trigger defines an event source for a project.
type Trigger struct {
	Type   string   `yaml:"type"`             // "push", "pull_request", "manual"
	Repo   string   `yaml:"repo,omitempty"`   // "owner/repo", required for push/pr
	Branch string   `yaml:"branch,omitempty"` // branch pattern
	PR     int      `yaml:"pr,omitempty"`     // specific PR number (pull_request type)
	On     []string `yaml:"on,omitempty"`     // ["push"], ["pull_request"]
}

// Project is a named collection of workflows with optional triggers.
type Project struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description,omitempty"`
	Workflows   []WorkflowRef `yaml:"workflows"`
	Triggers    []Trigger     `yaml:"triggers,omitempty"`
}

// EffectiveProjects returns Projects plus any legacy Watches converted to Projects.
func (c *Config) EffectiveProjects() []Project {
	result := make([]Project, 0, len(c.Projects)+len(c.Watches))
	result = append(result, c.Projects...)
	for _, w := range c.Watches {
		p := Project{
			Name:      w.Name,
			Workflows: w.Workflows,
		}
		if w.Repo != "" {
			trigType := "push"
			if w.PR > 0 {
				trigType = "pull_request"
			}
			on := w.On
			if len(on) == 0 {
				on = []string{"push"}
			}
			p.Triggers = append(p.Triggers, Trigger{
				Type:   trigType,
				Repo:   w.Repo,
				Branch: w.Branch,
				PR:     w.PR,
				On:     on,
			})
		}
		result = append(result, p)
	}
	return result
}

// HelloWorldWorkflow is default workflow content for new projects.
const HelloWorldWorkflow = `name: hello-world

on: [push]

jobs:
  hello:
    runs-on: ubuntu:22.04
    steps:
      - name: Hello
        run: echo "Hello from ghenkins! Project is working."
`

type Watch struct {
	Name      string        `yaml:"name"`
	Repo      string        `yaml:"repo"`
	PR        int           `yaml:"pr"`
	Branch    string        `yaml:"branch"`
	Workflows []WorkflowRef `yaml:"workflows"`
	On        []string      `yaml:"on"`
}

type WorkflowRef struct {
	Path           string            `yaml:"path"`
	Name           string            `yaml:"name"`
	Secrets        map[string]string `yaml:"secrets"`
	Env            map[string]string `yaml:"env"`
	RunnerImage    string            `yaml:"runner_image"` // overrides runs-on for this workflow
	TimeoutMinutes int               `yaml:"timeout_minutes"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	expanded, err := envsubst.EvalEnv(string(raw))
	if err != nil {
		return nil, fmt.Errorf("envsubst: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 2
	}
	if cfg.GitHub.PollInterval == 0 {
		cfg.GitHub.PollInterval = 30 * time.Second
	}
	if cfg.LogServer.Bind == "" {
		cfg.LogServer.Bind = "127.0.0.1:8765"
	}
	if cfg.LogServer.RetentionDays == 0 {
		cfg.LogServer.RetentionDays = 7
	}
	if cfg.LogServer.RetentionBytes == 0 {
		cfg.LogServer.RetentionBytes = 524288000
	}
	if cfg.Store.Path == "" {
		home, _ := os.UserHomeDir()
		cfg.Store.Path = filepath.Join(home, ".local", "share", "ghenkins", "ghenkins.db")
	}
	if cfg.Runner.DefaultImage == "" {
		cfg.Runner.DefaultImage = "ubuntu:22.04"
	}
	if cfg.Runner.WorkspaceDir == "" {
		if v := os.Getenv("GHENKINS_WORKSPACE_DIR"); v != "" {
			cfg.Runner.WorkspaceDir = v
		} else {
			home, _ := os.UserHomeDir()
			cfg.Runner.WorkspaceDir = filepath.Join(home, ".cache", "ghenkins", "workspace")
		}
	}
}

func validate(cfg *Config) error {
	for i, w := range cfg.Watches {
		if w.PR != 0 && w.Branch != "" {
			return fmt.Errorf("watch[%d] %q: cannot set both pr and branch", i, w.Name)
		}
		if w.PR == 0 && w.Branch == "" {
			return fmt.Errorf("watch[%d] %q: must set either pr or branch", i, w.Name)
		}
		parts := strings.SplitN(w.Repo, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("watch[%d] %q: repo must be in owner/repo form, got %q", i, w.Name, w.Repo)
		}
	}
	return nil
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ghenkins", "config.yaml")
}
