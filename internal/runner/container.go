package runner

import (
	"context"
	"fmt"
	"io"
	"os"

	dockerContainer "github.com/docker/docker/api/types/container"

	"github.com/containers/podman/v5/pkg/api/handlers"
	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/specgen"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// BindMount describes a bind mount from host to container.
type BindMount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// ContainerConfig holds configuration for creating a job container.
type ContainerConfig struct {
	Image         string
	Name          string            // unique name for this run
	Env           map[string]string
	WorkspaceHost string            // host path → mounted at /workspace
	RunnerDirHost string            // host path → mounted at /runner
	ExtraBinds    []BindMount       // additional mounts (e.g. composite action source)
	NetworkMode   string            // "bridge" (default) or "host"
}

// Container represents a running Podman container.
type Container struct {
	ID   string
	conn context.Context // podman bindings connection context
}

// Connect opens a connection to the Podman socket.
// Auto-detects socket at $XDG_RUNTIME_DIR/podman/podman.sock or /run/user/{uid}/podman/podman.sock.
func Connect(ctx context.Context) (context.Context, error) {
	sockDir := os.Getenv("XDG_RUNTIME_DIR")
	if sockDir == "" {
		sockDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	socket := "unix://" + sockDir + "/podman/podman.sock"
	return bindings.NewConnection(ctx, socket)
}

// PullImage pulls an image if not already present. Streams pull progress to logWriter.
func PullImage(conn context.Context, image string, logWriter io.Writer) error {
	fmt.Fprintf(logWriter, "## Pulling image %s...\n", image)
	_, err := images.Pull(conn, image, new(images.PullOptions))
	if err != nil {
		return err
	}
	fmt.Fprintf(logWriter, "## Image ready: %s\n", image)
	return nil
}

// CreateContainer creates a container from the given config (does not start it).
// Returns an error immediately if cfg.Image is empty.
func CreateContainer(conn context.Context, cfg ContainerConfig) (*Container, error) {
	if cfg.Image == "" {
		return nil, fmt.Errorf("container image must not be empty")
	}

	s := specgen.NewSpecGenerator(cfg.Image, false)
	s.Name = cfg.Name
	s.Entrypoint = []string{"/bin/sh"}
	s.Command = []string{"-c", "while true; do sleep 86400; done"}
	s.Env = cfg.Env
	s.WorkDir = "/workspace"

	mounts := []specs.Mount{
		{
			Type:        "bind",
			Source:      cfg.WorkspaceHost,
			Destination: "/workspace",
			Options:     []string{"rw"},
		},
		{
			Type:        "bind",
			Source:      cfg.RunnerDirHost,
			Destination: "/runner",
			Options:     []string{"rw"},
		},
	}
	for _, b := range cfg.ExtraBinds {
		opts := []string{"rw"}
		if b.ReadOnly {
			opts = []string{"ro"}
		}
		mounts = append(mounts, specs.Mount{
			Type:        "bind",
			Source:      b.HostPath,
			Destination: b.ContainerPath,
			Options:     opts,
		})
	}
	s.Mounts = mounts

	nsMode := specgen.Bridge
	if cfg.NetworkMode == "host" {
		nsMode = specgen.Host
	}
	s.NetNS = specgen.Namespace{NSMode: nsMode}

	resp, err := containers.CreateWithSpec(conn, s, nil)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}
	return &Container{ID: resp.ID, conn: conn}, nil
}

// Start starts the container.
func (c *Container) Start(ctx context.Context) error {
	return containers.Start(c.conn, c.ID, nil)
}

// Exec runs a command inside the running container and streams stdout/stderr to the writers.
// Returns the exit code. env is a list of "KEY=value" strings.
func (c *Container) Exec(ctx context.Context, cmd []string, env []string, workdir string, stdout, stderr io.Writer) (int, error) {
	config := &handlers.ExecCreateConfig{
		ExecOptions: dockerContainer.ExecOptions{
			Cmd:          cmd,
			Env:          env,
			WorkingDir:   workdir,
			AttachStdout: true,
			AttachStderr: true,
		},
	}
	sessionID, err := containers.ExecCreate(c.conn, c.ID, config)
	if err != nil {
		return -1, fmt.Errorf("exec create: %w", err)
	}

	opts := new(containers.ExecStartAndAttachOptions).
		WithOutputStream(stdout).
		WithErrorStream(stderr).
		WithAttachOutput(true).
		WithAttachError(true)
	if err := containers.ExecStartAndAttach(c.conn, sessionID, opts); err != nil {
		return -1, fmt.Errorf("exec start: %w", err)
	}

	info, err := containers.ExecInspect(c.conn, sessionID, nil)
	if err != nil {
		return -1, fmt.Errorf("exec inspect: %w", err)
	}
	return info.ExitCode, nil
}

// Remove stops (if running) and removes the container.
func (c *Container) Remove(ctx context.Context) error {
	timeout := uint(10)
	// Ignore stop errors (container may already be stopped).
	_ = containers.Stop(c.conn, c.ID, new(containers.StopOptions).WithTimeout(timeout))
	_, err := containers.Remove(c.conn, c.ID, new(containers.RemoveOptions).WithForce(true))
	return err
}
