package secrets

import (
	"context"
	"fmt"
	"strings"

	"github.com/99designs/keyring"
	"github.com/drone/envsubst"
)

// Store is the secret storage interface.
type Store interface {
	Set(ctx context.Context, service, key, value string) error
	Get(ctx context.Context, service, key string) (string, error)
	Delete(ctx context.Context, service, key string) error
	List(ctx context.Context, service string) ([]string, error)
}

// KeyringStore wraps 99designs/keyring using the OS keychain.
// Service name convention: "ghenkins/{watch-name}"
type KeyringStore struct {
	appName string
}

// New creates a KeyringStore for the given application name.
func New(appName string) (*KeyringStore, error) {
	return &KeyringStore{appName: appName}, nil
}

func (ks *KeyringStore) open(service string) (keyring.Keyring, error) {
	return keyring.Open(keyring.Config{
		ServiceName: fmt.Sprintf("%s/%s", ks.appName, service),
	})
}

func (ks *KeyringStore) Set(ctx context.Context, service, key, value string) error {
	kr, err := ks.open(service)
	if err != nil {
		return err
	}
	return kr.Set(keyring.Item{Key: key, Data: []byte(value)})
}

func (ks *KeyringStore) Get(ctx context.Context, service, key string) (string, error) {
	kr, err := ks.open(service)
	if err != nil {
		return "", err
	}
	item, err := kr.Get(key)
	if err != nil {
		return "", err
	}
	return string(item.Data), nil
}

func (ks *KeyringStore) Delete(ctx context.Context, service, key string) error {
	kr, err := ks.open(service)
	if err != nil {
		return err
	}
	return kr.Remove(key)
}

func (ks *KeyringStore) List(ctx context.Context, service string) ([]string, error) {
	kr, err := ks.open(service)
	if err != nil {
		return nil, err
	}
	return kr.Keys()
}

// ResolveSecrets resolves a map[string]string where values may be:
//   - Literal string → returned as-is
//   - "${ENV_VAR}" or "${ENV_VAR:-default}" → expanded via envsubst
//   - "keyring:{watch-name}:{key}" → fetched from keyring
func ResolveSecrets(ctx context.Context, raw map[string]string, ks Store) (map[string]string, error) {
	result := make(map[string]string, len(raw))
	for k, v := range raw {
		resolved, err := resolveValue(ctx, v, ks)
		if err != nil {
			return nil, fmt.Errorf("resolving secret %q: %w", k, err)
		}
		result[k] = resolved
	}
	return result, nil
}

func resolveValue(ctx context.Context, v string, ks Store) (string, error) {
	if strings.HasPrefix(v, "keyring:") {
		parts := strings.SplitN(v, ":", 3)
		if len(parts) != 3 {
			return "", fmt.Errorf("invalid keyring reference %q: expected keyring:<watch-name>:<key>", v)
		}
		return ks.Get(ctx, parts[1], parts[2])
	}
	if strings.Contains(v, "${") {
		return envsubst.EvalEnv(v)
	}
	return v, nil
}
