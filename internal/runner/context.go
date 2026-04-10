package runner

import (
	"encoding/json"
	"strconv"
	"strings"
)

// StepResult holds the result of a single step execution.
type StepResult struct {
	Outcome    string            // "success", "failure", "skipped", "cancelled"
	Conclusion string
	Outputs    map[string]string
}

// JobStatus represents the current overall job execution status.
type JobStatus int

const (
	JobStatusPending   JobStatus = iota
	JobStatusSuccess
	JobStatusFailure
	JobStatusCancelled
)

// EvalContext holds all GitHub Actions context objects for expression evaluation.
type EvalContext struct {
	Github    map[string]interface{}
	Env       map[string]string
	Job       map[string]interface{}
	Steps     map[string]StepResult // key = step ID
	Runner    map[string]interface{}
	Secrets   map[string]string
	Inputs    map[string]string
	JobStatus JobStatus
	Cancelled bool
}

// Lookup resolves a dotted path like "github.sha" or "steps.foo.outputs.bar"
// against the eval context. Returns (value, ok).
func (c *EvalContext) Lookup(parts []string) (interface{}, bool) {
	if len(parts) == 0 {
		return nil, false
	}
	switch parts[0] {
	case "github":
		return lookupValue(parts[1:], map[string]interface{}(c.Github))
	case "env":
		return lookupValue(parts[1:], stringsToInterface(c.Env))
	case "job":
		return lookupValue(parts[1:], map[string]interface{}(c.Job))
	case "steps":
		if len(parts) < 2 {
			return nil, false
		}
		step, ok := c.Steps[parts[1]]
		if !ok {
			return nil, false
		}
		return lookupValue(parts[2:], stepToMap(step))
	case "runner":
		return lookupValue(parts[1:], map[string]interface{}(c.Runner))
	case "secrets":
		return lookupValue(parts[1:], stringsToInterface(c.Secrets))
	case "inputs":
		return lookupValue(parts[1:], stringsToInterface(c.Inputs))
	}
	return nil, false
}

func stepToMap(s StepResult) map[string]interface{} {
	outputs := make(map[string]interface{}, len(s.Outputs))
	for k, v := range s.Outputs {
		outputs[k] = v
	}
	return map[string]interface{}{
		"outcome":    s.Outcome,
		"conclusion": s.Conclusion,
		"outputs":    outputs,
	}
}

func stringsToInterface(m map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// lookupValue traverses a nested value using parts. Parts encoded as "[N]" are array indexes.
// BuildEvalContext constructs the expression evaluation context for a given step.
// step may be nil when evaluating a job-level condition.
func BuildEvalContext(
	wf *Workflow,
	jobID string,
	job *Job,
	step *Step,
	stepResults map[string]StepResult,
	liveEnv map[string]string,
	info *JobInfo,
	secrets map[string]string,
	jobStatus JobStatus,
	cancelled bool,
) *EvalContext {
	githubCtx := map[string]interface{}{
		"sha":               info.SHA,
		"ref":               info.Ref,
		"ref_name":          info.RefName,
		"event_name":        info.EventName,
		"repository":        info.Repo,
		"repository_owner":  info.Owner,
		"actor":             info.Actor,
		"run_id":            info.RunID,
		"run_number":        info.RunNumber,
		"workspace":         "/workspace",
		"job":               jobID,
	}
	if len(info.EventPayload) > 0 {
		var event interface{}
		if err := json.Unmarshal(info.EventPayload, &event); err == nil {
			githubCtx["event"] = event
		}
	}

	mergedEnv := make(map[string]string)
	for k, v := range wf.Env {
		mergedEnv[k] = v
	}
	for k, v := range job.Env {
		mergedEnv[k] = v
	}
	for k, v := range liveEnv {
		mergedEnv[k] = v
	}
	if step != nil {
		for k, v := range step.Env {
			mergedEnv[k] = v
		}
	}

	runnerCtx := map[string]interface{}{
		"os":         "Linux",
		"arch":       "X64",
		"temp":       "/runner/_temp",
		"tool_cache": "/runner/_tools",
		"name":       "ghenkins",
	}

	jobCtx := map[string]interface{}{
		"status": jobStatusString(jobStatus),
	}

	inputs := make(map[string]string)
	if secrets == nil {
		secrets = make(map[string]string)
	}

	return &EvalContext{
		Github:    githubCtx,
		Env:       mergedEnv,
		Job:       jobCtx,
		Steps:     stepResults,
		Runner:    runnerCtx,
		Secrets:   secrets,
		Inputs:    inputs,
		JobStatus: jobStatus,
		Cancelled: cancelled,
	}
}

func jobStatusString(s JobStatus) string {
	switch s {
	case JobStatusSuccess, JobStatusPending:
		return "success"
	case JobStatusFailure:
		return "failure"
	case JobStatusCancelled:
		return "cancelled"
	default:
		return "success"
	}
}

func lookupValue(parts []string, val interface{}) (interface{}, bool) {
	if len(parts) == 0 {
		return val, true
	}
	part := parts[0]
	rest := parts[1:]

	// Array index: "[0]"
	if len(part) > 2 && part[0] == '[' && part[len(part)-1] == ']' {
		idx, err := strconv.Atoi(part[1 : len(part)-1])
		if err != nil {
			return nil, false
		}
		switch v := val.(type) {
		case []interface{}:
			if idx < 0 || idx >= len(v) {
				return nil, false
			}
			return lookupValue(rest, v[idx])
		}
		return nil, false
	}

	switch v := val.(type) {
	case map[string]interface{}:
		child, ok := v[part]
		if !ok {
			// Case-insensitive fallback
			lpart := strings.ToLower(part)
			for k, cv := range v {
				if strings.ToLower(k) == lpart {
					return lookupValue(rest, cv)
				}
			}
			return nil, false
		}
		return lookupValue(rest, child)
	}
	return nil, false
}
