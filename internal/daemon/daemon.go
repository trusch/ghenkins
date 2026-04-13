package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v67/github"
	"github.com/rs/zerolog"

	"github.com/trusch/ghenkins/internal/config"
	"github.com/trusch/ghenkins/internal/logserver"
	"github.com/trusch/ghenkins/internal/poller"
	"github.com/trusch/ghenkins/internal/queue"
	"github.com/trusch/ghenkins/internal/reporter"
	"github.com/trusch/ghenkins/internal/runner"
	"github.com/trusch/ghenkins/internal/secrets"
	"github.com/trusch/ghenkins/internal/store"
)

// staticTokenTransport injects a Bearer token into every request.
type staticTokenTransport struct {
	token string
}

func (t *staticTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req2)
}

type Daemon struct {
	cfg      *config.Config
	store    store.Store
	poller   *poller.Poller
	runner   runner.Runner
	reporter reporter.Reporter
	logSrv   *logserver.Server
	queue    *queue.Queue
	sem      chan struct{}
	log      zerolog.Logger
	inFlight sync.Map // map[runID string]context.CancelFunc
	wg       sync.WaitGroup
	jobs     chan poller.Job
}

func New(cfg *config.Config, log zerolog.Logger) (*Daemon, error) {
	// 1. Create parent dirs for cfg.Store.Path if needed.
	if err := os.MkdirAll(filepath.Dir(cfg.Store.Path), 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	// 2. Open SQLite store.
	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	// 3. Build GitHub client via static token transport.
	httpClient := &http.Client{Transport: &staticTokenTransport{token: cfg.GitHub.Token}}
	ghClient := github.NewClient(httpClient)

	// 4. Create runner.
	home, _ := os.UserHomeDir()
	cacheDir := filepath.Join(home, ".cache", "ghenkins", "repos")
	if err := os.MkdirAll(cfg.Runner.WorkspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace dir: %w", err)
	}
	r := runner.NewPodman(cacheDir, cfg.Runner.WorkspaceDir, cfg.Runner.DefaultImage, log)

	// 5. Create reporter.
	rep := reporter.New(ghClient, log)

	// 6. Determine logDir and create if missing.
	logDir := filepath.Join(home, ".local", "share", "ghenkins", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	// 7. Create log server.
	artifactDir := filepath.Join(home, ".cache", "ghenkins", "artifacts")
	logSrv := logserver.New(
		cfg.LogServer.Bind,
		logDir,
		artifactDir,
		st,
		cfg.LogServer.RetentionBytes,
		time.Duration(cfg.LogServer.RetentionDays)*24*time.Hour,
		log,
	)

	// 8. Create jobs channel (poller writes here).
	jobsCh := make(chan poller.Job, cfg.MaxConcurrency*4)

	// 9. Create poller.
	p := poller.New(cfg, ghClient, st, jobsCh, log)

	// 10. Create queue.
	q := queue.New(cfg.MaxConcurrency * 4)

	// 11. Create semaphore.
	sem := make(chan struct{}, cfg.MaxConcurrency)

	// 12. Prune orphaned worktrees.
	reposDir := filepath.Join(home, ".cache", "ghenkins", "repos")
	_ = filepath.WalkDir(reposDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || !strings.HasSuffix(path, ".git") {
			return nil
		}
		cmd := exec.Command("git", "-C", path, "worktree", "prune")
		_ = cmd.Run()
		return nil
	})

	return &Daemon{
		cfg:      cfg,
		store:    st,
		poller:   p,
		runner:   r,
		reporter: rep,
		logSrv:   logSrv,
		queue:    q,
		sem:      sem,
		log:      log,
		jobs:     jobsCh,
	}, nil
}

func (d *Daemon) recoverStaleRuns(ctx context.Context) {
	runs, err := d.store.ListRuns(ctx, 0) // 0 = all runs
	if err != nil {
		return
	}
	now := time.Now()
	for _, run := range runs {
		if run.Status == store.RunStatusQueued || run.Status == store.RunStatusRunning {
			d.log.Warn().Str("runID", run.ID).Str("status", string(run.Status)).Msg("recovering stale run")
			_ = d.store.UpdateRunStatus(ctx, run.ID, store.RunStatusError, nil, &now)
		}
	}
}

func (d *Daemon) seedWatchesFromConfig(ctx context.Context) error {
	projects, err := d.store.ListProjects(ctx)
	if err != nil {
		return err
	}
	if len(projects) > 0 {
		return nil
	}
	for _, p := range d.cfg.EffectiveProjects() {
		dbp := &store.DBProject{
			Name: p.Name,
		}
		for _, wf := range p.Workflows {
			dbp.Workflows = append(dbp.Workflows, &store.DBWorkflow{
				ProjectName:    p.Name,
				Name:           wf.Name,
				Path:           wf.Path,
				RunnerImage:    wf.RunnerImage,
				Secrets:        wf.Secrets,
				Env:            wf.Env,
				TimeoutMinutes: wf.TimeoutMinutes,
			})
		}
		for _, t := range p.Triggers {
			on := t.On
			if len(on) == 0 {
				on = []string{t.Type}
			}
			dbp.Triggers = append(dbp.Triggers, &store.DBTrigger{
				ProjectName: p.Name,
				Type:        t.Type,
				Repo:        t.Repo,
				Branch:      t.Branch,
				PR:          t.PR,
				OnEvents:    on,
			})
		}
		if err := d.store.CreateProject(ctx, dbp); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) Run(ctx context.Context) error {
	// 0a. Recover stale runs from previous crashes.
	d.recoverStaleRuns(ctx)

	// 0b. Set RunControl on log server so it can trigger/cancel runs.
	d.logSrv.SetRunControl(d)

	// 0c. Seed watches from config if DB is empty.
	if err := d.seedWatchesFromConfig(ctx); err != nil {
		d.log.Error().Err(err).Msg("seed watches from config")
	}

	// Configure poller to load watches from DB each cycle.
	d.poller.SetWatchProvider(func(ctx context.Context) ([]config.Watch, error) {
		projects, err := d.store.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		var watches []config.Watch
		for _, p := range projects {
			wfs := make([]config.WorkflowRef, 0, len(p.Workflows))
			for _, wf := range p.Workflows {
				wfs = append(wfs, config.WorkflowRef{
					Name:           wf.Name,
					Path:           wf.Path,
					RunnerImage:    wf.RunnerImage,
					Secrets:        wf.Secrets,
					Env:            wf.Env,
					TimeoutMinutes: wf.TimeoutMinutes,
				})
			}
			for _, t := range p.Triggers {
				if t.Type != "push" && t.Type != "pull_request" {
					continue
				}
				on := t.OnEvents
				if len(on) == 0 {
					on = []string{t.Type}
				}
				watches = append(watches, config.Watch{
					Name:      p.Name,
					Repo:      t.Repo,
					Branch:    t.Branch,
					PR:        t.PR,
					On:        on,
					Workflows: wfs,
				})
			}
		}
		return watches, nil
	})

	// 1. Start log server.
	go d.logSrv.Run(ctx) //nolint:errcheck

	// 2. Start poller.
	go d.poller.Run(ctx) //nolint:errcheck

	// 3. Forward jobs from channel into queue.
	go d.forwardJobs(ctx)

	// 4. Dispatch loop.
	for {
		job, err := d.queue.Dequeue(ctx)
		if err != nil {
			break
		}
		for _, wf := range job.WorkflowRefs {
			d.sem <- struct{}{}
			d.wg.Add(1)
			go d.runJob(ctx, job, wf)
		}
	}

	// 5. Drain and wait.
	d.queue.Close()
	d.wg.Wait()
	return d.store.Close()
}

func (d *Daemon) forwardJobs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-d.jobs:
			if !ok {
				return
			}
			d.queue.Enqueue(j)
		}
	}
}

func (d *Daemon) runJob(ctx context.Context, j poller.Job, wf config.WorkflowRef) {
	defer d.wg.Done()
	defer func() { <-d.sem }()

	runCtx, cancel := context.WithCancel(ctx)
	if wf.TimeoutMinutes > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(wf.TimeoutMinutes)*time.Minute)
	}
	runID := generateID()
	d.inFlight.Store(runID, cancel)
	defer func() { cancel(); d.inFlight.Delete(runID) }()

	owner, repoName := splitRepo(j.Repo)
	logPath := d.logSrv.LogPath(runID)

	// Create run record.
	now := time.Now()
	run := &store.Run{
		ID:           runID,
		WatchName:    j.WatchName,
		Repo:         j.Repo,
		SHA:          j.SHA,
		WorkflowName: wf.Name,
		Status:       store.RunStatusQueued,
		StartedAt:    now,
		LogPath:      logPath,
	}
	d.store.CreateRun(runCtx, run) //nolint:errcheck

	targetURL := fmt.Sprintf("http://%s/runs/%s/log", d.cfg.LogServer.Bind, runID)

	// Post pending/queued status.
	d.reporter.Report(runCtx, reporter.ReportRequest{ //nolint:errcheck
		Owner: owner, Repo: repoName, SHA: j.SHA, WorkflowName: wf.Name,
		Status: reporter.StatusPending, Description: "queued",
		TargetURL: targetURL,
	})

	// Resolve secrets.
	secretStore, _ := secrets.New("ghenkins")
	resolved, _ := secrets.ResolveSecrets(runCtx, wf.Secrets, secretStore)
	wf.Secrets = resolved

	// Mark running.
	d.store.UpdateRunStatus(runCtx, runID, store.RunStatusRunning, nil, nil) //nolint:errcheck
	d.reporter.Report(runCtx, reporter.ReportRequest{                        //nolint:errcheck
		Owner: owner, Repo: repoName, SHA: j.SHA, WorkflowName: wf.Name,
		Status: reporter.StatusPending, Description: "running",
		TargetURL: targetURL,
	})

	// Set job-level callback and artifact wiring if the runner supports it.
	if pr, ok := d.runner.(*runner.PodmanRunner); ok {
		pr.CurrentRunID = runID
		pr.Store = d.store
		capturedRunID := runID
		pr.JobCallback = func(jobName string, status runner.JobStatus, startedAt, finishedAt time.Time) {
			rj := &store.RunJob{
				ID:      capturedRunID + "-" + jobName,
				RunID:   capturedRunID,
				JobName: jobName,
				Status:  store.RunJobStatus(jobStatusToString(status)),
			}
			if !startedAt.IsZero() {
				t := startedAt
				rj.StartedAt = &t
			}
			if !finishedAt.IsZero() {
				t := finishedAt
				rj.FinishedAt = &t
			}
			_ = d.store.UpsertRunJob(context.Background(), rj)
		}
	}

	// Open log file; fall back to io.Discard so a create failure does not panic.
	var logWriter io.Writer = io.Discard
	logFile, fileErr := os.Create(logPath)
	if fileErr != nil {
		d.log.Error().Err(fileErr).Str("runID", runID).Msg("failed to create log file")
	} else {
		logWriter = logFile
	}
	exitCode, err := d.runner.Run(runCtx, j, wf, logWriter)
	if logFile != nil {
		logFile.Close()
	}

	// Determine final status.
	finishedAt := time.Now()
	var finalStatus store.RunStatus
	var reportStatus reporter.Status
	var desc string
	if err != nil {
		finalStatus = store.RunStatusError
		reportStatus = reporter.StatusError
		desc = "internal error: " + err.Error()
		// Write the error to the log so users can see it.
		if logFile2, openErr := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); openErr == nil {
			fmt.Fprintf(logFile2, "## ERROR: %v\n", err)
			logFile2.Close()
		}
	} else if exitCode == 0 {
		finalStatus = store.RunStatusSuccess
		reportStatus = reporter.StatusSuccess
		desc = fmt.Sprintf("passed in %s", finishedAt.Sub(now).Round(time.Second))
	} else {
		finalStatus = store.RunStatusFailure
		reportStatus = reporter.StatusFailure
		desc = fmt.Sprintf("failed (exit %d)", exitCode)
	}

	d.store.UpdateRunStatus(runCtx, runID, finalStatus, &exitCode, &finishedAt) //nolint:errcheck
	d.reporter.Report(runCtx, reporter.ReportRequest{                            //nolint:errcheck
		Owner: owner, Repo: repoName, SHA: j.SHA, WorkflowName: wf.Name,
		Status: reportStatus, Description: desc,
		TargetURL: targetURL,
	})
}

// TriggerRun implements RunControl. It enqueues a manual job for the given project/workflow.
func (d *Daemon) TriggerRun(ctx context.Context, projectName, workflowName string, inputs map[string]string) (string, error) {
	project, err := d.store.GetProject(ctx, projectName)
	if err != nil {
		return "", fmt.Errorf("project %q not found", projectName)
	}

	var wfRef *store.DBWorkflow
	for _, wf := range project.Workflows {
		if wf.Name == workflowName {
			wfRef = wf
			break
		}
	}
	if wfRef == nil {
		return "", fmt.Errorf("workflow %q not found in project %q", workflowName, projectName)
	}

	// Determine repo and branch from the first push/pr trigger, if any.
	var repo, branch string
	for _, t := range project.Triggers {
		if t.Type == "push" || t.Type == "pull_request" {
			repo = t.Repo
			branch = t.Branch
			break
		}
	}

	var owner, repoName string
	if repo != "" {
		owner, repoName = splitRepo(repo)
	}

	job := poller.Job{
		WatchName: projectName,
		Repo:      repo,
		Owner:     owner,
		RepoName:  repoName,
		SHA:       "manual",
		Branch:    branch,
		EventType: "manual",
		WorkflowRefs: []config.WorkflowRef{{
			Name:           wfRef.Name,
			Path:           wfRef.Path,
			RunnerImage:    wfRef.RunnerImage,
			Secrets:        wfRef.Secrets,
			Env:            wfRef.Env,
			TimeoutMinutes: wfRef.TimeoutMinutes,
		}},
		Inputs: inputs,
	}

	select {
	case d.jobs <- job:
	default:
		return "", fmt.Errorf("job queue full")
	}

	return "", nil
}

// CancelRun implements RunControl. It cancels the in-flight run with the given ID.
func (d *Daemon) CancelRun(ctx context.Context, runID string) error {
	val, ok := d.inFlight.Load(runID)
	if !ok {
		return fmt.Errorf("run %q not found or already finished", runID)
	}
	cancel := val.(context.CancelFunc)
	cancel()
	now := time.Now()
	_ = d.store.UpdateRunStatus(ctx, runID, store.RunStatusCanceled, nil, &now)
	return nil
}

func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func splitRepo(repo string) (string, string) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) < 2 {
		return "", parts[0]
	}
	return parts[0], parts[1]
}

func jobStatusToString(s runner.JobStatus) string {
	switch s {
	case runner.JobStatusSuccess:
		return "success"
	case runner.JobStatusFailure:
		return "failure"
	case runner.JobStatusCancelled:
		return "failure"
	case runner.JobStatusSkipped:
		return "skipped"
	case runner.JobStatusRunning:
		return "running"
	default:
		return "pending"
	}
}
