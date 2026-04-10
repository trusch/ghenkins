//go:build integration

package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConnect_Integration(t *testing.T) {
	conn, err := Connect(context.Background())
	require.NoError(t, err)
	_ = conn
}
