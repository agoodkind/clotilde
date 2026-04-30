package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"
)

type daemonExecutableSnapshot struct {
	path    string
	info    os.FileInfo
	content string
}

func (s *Server) startBinaryUpdateWatcher(interval time.Duration) func() {
	ctx, cancel := context.WithCancel(context.Background())
	base, err := currentDaemonExecutableSnapshot()
	if err != nil {
		s.log.Warn("daemon.binary_update.snapshot_failed",
			"component", "daemon",
			"err", err)
		return cancel
	}
	s.log.Debug("daemon.binary_update.watcher_started",
		"component", "daemon",
		"path", base.path,
		"mtime", base.info.ModTime().Format(time.RFC3339Nano),
		"size", base.info.Size(),
		"hash", base.content)
	go s.runBinaryUpdateWatcher(ctx, interval, base)
	return cancel
}

func (s *Server) runBinaryUpdateWatcher(ctx context.Context, interval time.Duration, base daemonExecutableSnapshot) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			changed, reason, next, err := daemonExecutableChanged(base)
			if err != nil {
				s.log.Debug("daemon.binary_update.check_failed",
					"component", "daemon",
					"path", base.path,
					"err", err)
				continue
			}
			if !changed {
				continue
			}
			s.log.LogAttrs(context.Background(), slog.LevelDebug, "daemon.binary_update.detected",
				slog.String("component", "daemon"),
				slog.String("path", next.path),
				slog.String("reason", reason),
				slog.String("hash", next.content))
			s.publishBinaryUpdate(next.path, reason, next.content)
			return
		}
	}
}

func currentDaemonExecutableSnapshot() (daemonExecutableSnapshot, error) {
	path, err := os.Executable()
	if err != nil {
		return daemonExecutableSnapshot{}, fmt.Errorf("resolve executable: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return daemonExecutableSnapshot{}, fmt.Errorf("stat executable: %w", err)
	}
	hash, err := shortFileSHA256(path)
	if err != nil {
		return daemonExecutableSnapshot{}, fmt.Errorf("hash executable: %w", err)
	}
	return daemonExecutableSnapshot{path: path, info: info, content: hash}, nil
}

func daemonExecutableChanged(base daemonExecutableSnapshot) (bool, string, daemonExecutableSnapshot, error) {
	next, err := currentDaemonExecutableSnapshot()
	if err != nil {
		return false, "", daemonExecutableSnapshot{}, err
	}
	if !os.SameFile(base.info, next.info) {
		return true, "file_replaced", next, nil
	}
	if !next.info.ModTime().Equal(base.info.ModTime()) {
		return true, "mtime_changed", next, nil
	}
	if next.info.Size() != base.info.Size() {
		return true, "size_changed", next, nil
	}
	if next.content != base.content {
		return true, "content_changed", next, nil
	}
	return false, "", next, nil
}

func shortFileSHA256(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:6], nil
}
