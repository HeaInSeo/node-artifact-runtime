package runtimehelper

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return atomicWriteFileContext(context.Background(), path, data, perm)
}

func atomicWriteFileContext(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// Validate that the manifest directory doesn't traverse a symlink to prevent
	// an attacker from redirecting writes outside the expected directory.
	if resolved, err := filepath.EvalSymlinks(dir); err != nil {
		return fmt.Errorf("manifest dir symlink check: %w", err)
	} else if resolved != filepath.Clean(dir) {
		return fmt.Errorf("manifest dir %q resolves to %q via symlink; refusing write", dir, resolved)
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func writeTerminationSummary(cfg Config, summary TerminationSummary) {
	if cfg.TerminationLogPath == "" {
		return
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return
	}
	if err := writeTerminationLog(cfg.TerminationLogPath, append(raw, '\n')); err != nil {
		_, _ = fmt.Fprintf(stderrOrDefault(cfg.Stderr), "warning: failed to write termination log: %v\n", err)
	}
}

func writeTerminationManifest(cfg Config) {
	if cfg.TerminationLogPath == "" || cfg.ManifestPath == "" {
		return
	}
	raw, err := os.ReadFile(cfg.ManifestPath)
	if err != nil {
		return
	}
	if len(raw) == 0 {
		return
	}
	if raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	if err := writeTerminationLog(cfg.TerminationLogPath, raw); err != nil {
		_, _ = fmt.Fprintf(stderrOrDefault(cfg.Stderr), "warning: failed to write termination manifest: %v\n", err)
	}
}

func writeTerminationLog(path string, data []byte) error {
	if path == "" {
		return nil
	}
	return os.WriteFile(path, data, 0o600) //nolint:gosec // path is operator-controlled (/dev/termination-log default or explicit flag)
}
