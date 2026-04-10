package runner

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// EnvFiles tracks read offsets into the three magic files on the host.
// The files are created empty before each job and bind-mounted into the container at /runner/.
type EnvFiles struct {
	dir       string // host path of the runner dir (mounted as /runner in container)
	envOff    int64
	outputOff int64
	pathOff   int64
}

// NewEnvFiles creates the four magic files (touching them empty) in dir.
// Files: _env, _output, _path, _summary
// Returns an EnvFiles ready to track reads from offset 0.
func NewEnvFiles(dir string) (*EnvFiles, error) {
	for _, name := range []string{"_env", "_output", "_path", "_summary"} {
		f, err := os.OpenFile(dir+"/"+name, os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", name, err)
		}
		f.Close()
	}
	return &EnvFiles{dir: dir}, nil
}

// ReadDelta reads any entries written to the magic files since the last call.
// Returns new env vars, new step outputs, new PATH entries.
// Updates internal offsets so the next call only returns new data.
func (f *EnvFiles) ReadDelta() (env map[string]string, outputs map[string]string, paths []string, err error) {
	envData, newEnvOff, err := readNewBytes(f.dir+"/_env", f.envOff)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read _env: %w", err)
	}

	outputData, newOutputOff, err := readNewBytes(f.dir+"/_output", f.outputOff)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read _output: %w", err)
	}

	pathData, newPathOff, err := readNewBytes(f.dir+"/_path", f.pathOff)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read _path: %w", err)
	}

	env, err = parseEnvFile(envData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse _env: %w", err)
	}

	outputs, err = parseEnvFile(outputData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse _output: %w", err)
	}

	paths = parsePathFile(pathData)

	f.envOff = newEnvOff
	f.outputOff = newOutputOff
	f.pathOff = newPathOff

	return env, outputs, paths, nil
}

// readNewBytes reads bytes from the file starting at offset, returns the data and new offset.
func readNewBytes(path string, offset int64) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()

	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, offset, err
	}

	return data, offset + int64(len(data)), nil
}

// parsePathFile splits path data into individual path entries (one per line).
func parsePathFile(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// parseEnvFile parses the KEY=value / heredoc format from data.
// Supports:
//
//	KEY=value
//	KEY<<DELIMITER
//	line1
//	line2
//	DELIMITER
func parseEnvFile(data []byte) (map[string]string, error) {
	result := make(map[string]string)
	if len(data) == 0 {
		return result, nil
	}

	lines := strings.Split(string(bytes.TrimRight(data, "\n")), "\n")
	// Re-add the trailing newline we trimmed so we can detect partial lines.
	// Actually, we want to process all complete lines. We split on \n and skip empty trailing.
	// Reset: split without trimming to handle multiline heredoc properly.
	lines = strings.Split(string(data), "\n")

	i := 0
	for i < len(lines) {
		line := lines[i]

		// Skip empty lines
		if line == "" {
			i++
			continue
		}

		// Check for heredoc: KEY<<DELIMITER
		if idx := strings.Index(line, "<<"); idx != -1 {
			key := line[:idx]
			delimiter := line[idx+2:]
			if key != "" && delimiter != "" {
				i++
				var valueBuf strings.Builder
				for i < len(lines) {
					if lines[i] == delimiter {
						i++
						break
					}
					valueBuf.WriteString(lines[i])
					valueBuf.WriteByte('\n')
					i++
				}
				result[key] = valueBuf.String()
				continue
			}
		}

		// Simple KEY=value
		if idx := strings.Index(line, "="); idx != -1 {
			key := line[:idx]
			value := line[idx+1:]
			if key != "" {
				result[key] = value
			}
		}

		i++
	}

	return result, nil
}
