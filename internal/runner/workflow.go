package runner

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type Workflow struct {
	Name     string            `yaml:"name"`
	On       interface{}       `yaml:"on"`
	Env      map[string]string `yaml:"env"`
	Defaults *Defaults         `yaml:"defaults"`
	Jobs     map[string]*Job   `yaml:"jobs"`
}

type Defaults struct {
	Run RunDefaults `yaml:"run"`
}

type RunDefaults struct {
	Shell            string `yaml:"shell"`
	WorkingDirectory string `yaml:"working-directory"`
}

type JobBindMount struct {
	Host      string `yaml:"host"`
	Container string `yaml:"container"`
	ReadOnly  bool   `yaml:"read-only"`
}

type Job struct {
	Name           string            `yaml:"name"`
	RunsOn         interface{}       `yaml:"runs-on"`
	Needs          interface{}       `yaml:"needs"`
	If             string            `yaml:"if"`
	Env            map[string]string `yaml:"env"`
	Defaults       *Defaults         `yaml:"defaults"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Steps          []*Step           `yaml:"steps"`
	ExtraBinds     []JobBindMount    `yaml:"extra-binds"`
}

type Step struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Uses             string            `yaml:"uses"`
	Run              string            `yaml:"run"`
	Shell            string            `yaml:"shell"`
	WorkingDirectory string            `yaml:"working-directory"`
	With             map[string]string `yaml:"with"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	TimeoutMinutes   int               `yaml:"timeout-minutes"`
}

type ActionDef struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Inputs      map[string]ActionInput  `yaml:"inputs"`
	Outputs     map[string]ActionOutput `yaml:"outputs"`
	Runs        ActionRuns             `yaml:"runs"`
}

type ActionInput struct {
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
}

type ActionOutput struct {
	Description string `yaml:"description"`
	Value       string `yaml:"value"`
}

type ActionRuns struct {
	Using string  `yaml:"using"`
	Steps []*Step `yaml:"steps"`
	Main  string  `yaml:"main"`
	Image string  `yaml:"image"`
}

// ParseWorkflow parses a GitHub Actions workflow YAML file.
func ParseWorkflow(data []byte) (*Workflow, error) {
	var wf Workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	return &wf, nil
}

// ParseActionDef parses an action.yml file.
func ParseActionDef(data []byte) (*ActionDef, error) {
	var def ActionDef
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("parse action def: %w", err)
	}
	return &def, nil
}

// JobNeeds returns the list of job IDs that jobID depends on.
func (w *Workflow) JobNeeds(jobID string) []string {
	job, ok := w.Jobs[jobID]
	if !ok {
		return nil
	}
	return job.NeedsIDs()
}

// RunsOnImage returns the first string from a runs-on field (handles string or []string).
func (j *Job) RunsOnImage() string {
	switch v := j.RunsOn.(type) {
	case string:
		return v
	case []interface{}:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

// NeedsIDs returns job IDs from Needs field (handles string or []string).
func (j *Job) NeedsIDs() []string {
	switch v := j.Needs.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []interface{}:
		ids := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				ids = append(ids, s)
			}
		}
		return ids
	}
	return nil
}

// BuildExecutionOrder returns job IDs in topological order using Kahn's algorithm.
// Each inner slice contains jobs that can run concurrently.
// Returns an error if a cycle is detected.
func BuildExecutionOrder(jobs map[string]*Job) ([][]string, error) {
	// Build in-degree map and adjacency list
	inDegree := make(map[string]int, len(jobs))
	dependents := make(map[string][]string, len(jobs)) // id -> list of jobs that depend on id

	for id := range jobs {
		if _, ok := inDegree[id]; !ok {
			inDegree[id] = 0
		}
	}

	for id, job := range jobs {
		for _, need := range job.NeedsIDs() {
			if _, ok := jobs[need]; !ok {
				return nil, fmt.Errorf("job %q needs unknown job %q", id, need)
			}
			inDegree[id]++
			dependents[need] = append(dependents[need], id)
		}
	}

	// Kahn's algorithm
	var result [][]string
	processed := 0

	for {
		// Collect all nodes with in-degree 0
		var wave []string
		for id, deg := range inDegree {
			if deg == 0 {
				wave = append(wave, id)
			}
		}
		if len(wave) == 0 {
			break
		}

		// Remove from inDegree so we don't pick them again
		for _, id := range wave {
			delete(inDegree, id)
		}

		result = append(result, wave)
		processed += len(wave)

		// Decrement in-degree for dependents
		for _, id := range wave {
			for _, dep := range dependents[id] {
				inDegree[dep]--
			}
		}
	}

	if processed != len(jobs) {
		return nil, fmt.Errorf("cycle detected in job dependencies")
	}

	return result, nil
}
