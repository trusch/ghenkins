package secrets_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trusch/ghenkins/internal/secrets"
)

type mockStore struct {
	data map[string]map[string]string
}

func newMockStore() *mockStore {
	return &mockStore{data: make(map[string]map[string]string)}
}

func (m *mockStore) Set(_ context.Context, service, key, value string) error {
	if m.data[service] == nil {
		m.data[service] = make(map[string]string)
	}
	m.data[service][key] = value
	return nil
}

func (m *mockStore) Get(_ context.Context, service, key string) (string, error) {
	svc, ok := m.data[service]
	if !ok {
		return "", errors.New("service not found")
	}
	v, ok := svc[key]
	if !ok {
		return "", errors.New("key not found")
	}
	return v, nil
}

func (m *mockStore) Delete(_ context.Context, service, key string) error {
	if m.data[service] != nil {
		delete(m.data[service], key)
	}
	return nil
}

func (m *mockStore) List(_ context.Context, service string) ([]string, error) {
	svc, ok := m.data[service]
	if !ok {
		return nil, nil
	}
	keys := make([]string, 0, len(svc))
	for k := range svc {
		keys = append(keys, k)
	}
	return keys, nil
}

func TestResolveSecrets(t *testing.T) {
	ctx := context.Background()
	store := newMockStore()
	_ = store.Set(ctx, "watch", "MY_KEY", "secret-value")

	t.Run("literal passes through", func(t *testing.T) {
		result, err := secrets.ResolveSecrets(ctx, map[string]string{"KEY": "literal"}, store)
		require.NoError(t, err)
		assert.Equal(t, "literal", result["KEY"])
	})

	t.Run("env var expanded", func(t *testing.T) {
		t.Setenv("TEST_VAR", "expanded")
		result, err := secrets.ResolveSecrets(ctx, map[string]string{"KEY": "${TEST_VAR}"}, store)
		require.NoError(t, err)
		assert.Equal(t, "expanded", result["KEY"])
	})

	t.Run("env var default when unset", func(t *testing.T) {
		os.Unsetenv("UNSET_VAR")
		result, err := secrets.ResolveSecrets(ctx, map[string]string{"KEY": "${UNSET_VAR:-fallback}"}, store)
		require.NoError(t, err)
		assert.Equal(t, "fallback", result["KEY"])
	})

	t.Run("keyring fetched from store", func(t *testing.T) {
		result, err := secrets.ResolveSecrets(ctx, map[string]string{"KEY": "keyring:watch:MY_KEY"}, store)
		require.NoError(t, err)
		assert.Equal(t, "secret-value", result["KEY"])
	})

	t.Run("missing keyring key returns error", func(t *testing.T) {
		_, err := secrets.ResolveSecrets(ctx, map[string]string{"KEY": "keyring:watch:MISSING"}, store)
		require.Error(t, err)
	})
}
