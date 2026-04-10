package runner

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnect_MissingSocket verifies that Connect returns an error when the
// Podman socket does not exist at the resolved path.
func TestConnect_MissingSocket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	// dir/podman/podman.sock does not exist
	_, err := Connect(context.Background())
	require.Error(t, err, "Connect should fail when socket is absent")
}

// TestCreateContainer_EmptyImage verifies that CreateContainer rejects an
// empty image name before making any Podman API call.
func TestCreateContainer_EmptyImage(t *testing.T) {
	// Use a dummy context — no real connection needed because validation
	// fires before the API call.
	conn := context.Background()
	cfg := ContainerConfig{
		Image:         "",
		WorkspaceHost: os.TempDir(),
		RunnerDirHost: os.TempDir(),
	}
	_, err := CreateContainer(conn, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image")
}
