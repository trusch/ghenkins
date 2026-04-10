package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/drone/envsubst"
)

// Store is the secret storage interface.
type Store interface {
	Set(ctx context.Context, service, key, value string) error
	Get(ctx context.Context, service, key string) (string, error)
	Delete(ctx context.Context, service, key string) error
	List(ctx context.Context, service string) ([]string, error)
}

// FileStore stores secrets as JSON files under a base directory.
// Path: {baseDir}/{service}/{key}.json
type FileStore struct {
	baseDir string
}

// New creates a FileStore rooted at ~/.local/share/{appName}/secrets.
func New(appName string) (*FileStore, error) {
	base, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, ".local", "share", appName, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &FileStore{baseDir: dir}, nil
}

func (fs *FileStore) path(service, key string) string {
	return filepath.Join(fs.baseDir, filepath.Clean(service), key+".json")
}

func (fs *FileStore) Set(_ context.Context, service, key, value string) error {
	dir := filepath.Join(fs.baseDir, filepath.Clean(service))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(fs.path(service, key), data, 0600)
}

func (fs *FileStore) Get(_ context.Context, service, key string) (string, error) {
	data, err := os.ReadFile(fs.path(service, key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("secret %q not found in service %q", key, service)
		}
		return "", err
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	return value, nil
}

func (fs *FileStore) Delete(_ context.Context, service, key string) error {
	err := os.Remove(fs.path(service, key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (fs *FileStore) List(_ context.Context, service string) ([]string, error) {
	dir := filepath.Join(fs.baseDir, filepath.Clean(service))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			keys = append(keys, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	return keys, nil
}

// ResolveSecrets resolves a map[string]string where values may be:
//   - Literal string → returned as-is
//   - "${ENV_VAR}" or "${ENV_VAR:-default}" → expanded via envsubst
//   - "keyring:{watch-name}:{key}" → fetched from store (uses FileStore)
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
