package logserver

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// runRetention runs the retention policy every hour until ctx is done.
func (s *Server) runRetention(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	s.ApplyRetention()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ApplyRetention()
		}
	}
}

type logFile struct {
	path  string
	size  int64
	mtime time.Time
}

// ApplyRetention enforces the configured age and size retention policies.
func (s *Server) ApplyRetention() {
	entries, err := os.ReadDir(s.logDir)
	if err != nil {
		if !os.IsNotExist(err) {
			s.log.Error().Err(err).Msg("retention: read log dir")
		}
		return
	}

	var files []logFile
	var totalSize int64

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fi := logFile{
			path:  filepath.Join(s.logDir, entry.Name()),
			size:  info.Size(),
			mtime: info.ModTime(),
		}
		// Delete files older than maxAge.
		if time.Since(fi.mtime) > s.maxAge {
			if err := os.Remove(fi.path); err == nil {
				s.log.Info().Str("path", fi.path).Msg("retention: removed old log")
			}
			continue
		}
		files = append(files, fi)
		totalSize += fi.size
	}

	// Sort ascending by mtime so we delete oldest first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.Before(files[j].mtime)
	})

	// Delete oldest until total size is within limit.
	for totalSize > s.maxBytes && len(files) > 0 {
		f := files[0]
		files = files[1:]
		if err := os.Remove(f.path); err == nil {
			totalSize -= f.size
			s.log.Info().Str("path", f.path).Int64("freed", f.size).Msg("retention: removed log for size limit")
		}
	}
}
