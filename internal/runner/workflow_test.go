package runner

import (
	"testing"
)

func TestParseWorkflow_MinimalPush(t *testing.T) {
	data := []byte(`
name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: Run tests
        run: go test ./...
`)
	wf, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Name != "CI" {
		t.Errorf("name: got %q, want %q", wf.Name, "CI")
	}
	job, ok := wf.Jobs["build"]
	if !ok {
		t.Fatal("missing job 'build'")
	}
	if job.RunsOnImage() != "ubuntu-latest" {
		t.Errorf("runs-on: got %q, want %q", job.RunsOnImage(), "ubuntu-latest")
	}
	if len(job.Steps) != 1 {
		t.Fatalf("steps: got %d, want 1", len(job.Steps))
	}
	if job.Steps[0].Run != "go test ./..." {
		t.Errorf("step run: got %q", job.Steps[0].Run)
	}
}

func TestParseWorkflow_Needs(t *testing.T) {
	data := []byte(`
name: Pipeline
on: push
jobs:
  setup:
    runs-on: ubuntu-latest
    steps:
      - run: echo setup
  build:
    runs-on: ubuntu-latest
    needs: setup
    steps:
      - run: echo build
  test:
    runs-on: ubuntu-latest
    needs: [setup, build]
    steps:
      - run: echo test
`)
	wf, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	buildNeeds := wf.JobNeeds("build")
	if len(buildNeeds) != 1 || buildNeeds[0] != "setup" {
		t.Errorf("build needs: got %v, want [setup]", buildNeeds)
	}
	testNeeds := wf.JobNeeds("test")
	if len(testNeeds) != 2 {
		t.Errorf("test needs: got %v, want [setup build]", testNeeds)
	}
}

func TestParseWorkflow_CompositeAction(t *testing.T) {
	data := []byte(`
name: With Action
on: push
jobs:
  use-action:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: ./action
        with:
          token: ${{ secrets.GITHUB_TOKEN }}
`)
	wf, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job := wf.Jobs["use-action"]
	if job == nil {
		t.Fatal("missing job 'use-action'")
	}
	step := job.Steps[0]
	if step.Uses != "./action" {
		t.Errorf("uses: got %q, want %q", step.Uses, "./action")
	}
	if step.With["token"] != "${{ secrets.GITHUB_TOKEN }}" {
		t.Errorf("with.token: got %q", step.With["token"])
	}
}

func TestParseWorkflow_EnvLevels(t *testing.T) {
	data := []byte(`
name: Env Test
on: push
env:
  GLOBAL: global_val
  SHARED: overridden
jobs:
  job1:
    runs-on: ubuntu-latest
    env:
      JOB_VAR: job_val
      SHARED: job_override
    steps:
      - name: Step
        run: echo hi
        env:
          STEP_VAR: step_val
`)
	wf, err := ParseWorkflow(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Env["GLOBAL"] != "global_val" {
		t.Errorf("workflow env GLOBAL: got %q", wf.Env["GLOBAL"])
	}
	job := wf.Jobs["job1"]
	if job.Env["JOB_VAR"] != "job_val" {
		t.Errorf("job env JOB_VAR: got %q", job.Env["JOB_VAR"])
	}
	if job.Env["SHARED"] != "job_override" {
		t.Errorf("job env SHARED: got %q", job.Env["SHARED"])
	}
	step := job.Steps[0]
	if step.Env["STEP_VAR"] != "step_val" {
		t.Errorf("step env STEP_VAR: got %q", step.Env["STEP_VAR"])
	}
}

func TestBuildExecutionOrder_LinearChain(t *testing.T) {
	jobs := map[string]*Job{
		"a": {Needs: nil},
		"b": {Needs: "a"},
		"c": {Needs: "b"},
	}
	order, err := BuildExecutionOrder(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be 3 waves: [a], [b], [c]
	if len(order) != 3 {
		t.Fatalf("waves: got %d, want 3: %v", len(order), order)
	}
	if len(order[0]) != 1 || order[0][0] != "a" {
		t.Errorf("wave 0: got %v, want [a]", order[0])
	}
	if len(order[1]) != 1 || order[1][0] != "b" {
		t.Errorf("wave 1: got %v, want [b]", order[1])
	}
	if len(order[2]) != 1 || order[2][0] != "c" {
		t.Errorf("wave 2: got %v, want [c]", order[2])
	}
}

func TestBuildExecutionOrder_DiamondDAG(t *testing.T) {
	// a -> b, a -> c, b -> d, c -> d
	jobs := map[string]*Job{
		"a": {Needs: nil},
		"b": {Needs: "a"},
		"c": {Needs: "a"},
		"d": {Needs: []interface{}{"b", "c"}},
	}
	order, err := BuildExecutionOrder(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Wave 0: [a], wave 1: [b, c] (any order), wave 2: [d]
	if len(order) != 3 {
		t.Fatalf("waves: got %d, want 3: %v", len(order), order)
	}
	if len(order[0]) != 1 || order[0][0] != "a" {
		t.Errorf("wave 0: got %v, want [a]", order[0])
	}
	if len(order[1]) != 2 {
		t.Errorf("wave 1: got %v, want [b c]", order[1])
	}
	if len(order[2]) != 1 || order[2][0] != "d" {
		t.Errorf("wave 2: got %v, want [d]", order[2])
	}
}

func TestBuildExecutionOrder_CycleDetection(t *testing.T) {
	jobs := map[string]*Job{
		"a": {Needs: "b"},
		"b": {Needs: "a"},
	}
	_, err := BuildExecutionOrder(jobs)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}
