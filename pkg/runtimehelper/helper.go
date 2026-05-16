package runtimehelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
)

// Config describes the runtime-side artifact helper contract executed inside
// the DAG node runtime container after wrapping the user command.
type Config struct {
	RunID              string
	SampleRunID        string
	NodeID             string
	OutputNames        []string
	OutputRoot         string
	ManifestPath       string
	TerminationLogPath string
	Command            []string
	Stdout             io.Writer
	Stderr             io.Writer
}

func (c Config) Validate() error {
	if c.RunID == "" {
		return fmt.Errorf("runID is required")
	}
	if c.NodeID == "" {
		return fmt.Errorf("nodeID is required")
	}
	if len(c.Command) == 0 {
		return fmt.Errorf("command is required")
	}
	if c.OutputRoot == "" {
		return fmt.Errorf("outputRoot is required")
	}
	if c.ManifestPath == "" {
		return fmt.Errorf("manifestPath is required")
	}
	return nil
}

func Run(ctx context.Context, cfg Config) int {
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		return 2
	}

	// #nosec G204 -- runtimehelper intentionally executes the node command selected by the run spec.
	cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
	cmd.Stdout = stdoutOrDefault(cfg.Stdout)
	cmd.Stderr = stderrOrDefault(cfg.Stderr)
	if err := cmd.Run(); err != nil {
		return exitCode(err)
	}

	if err := emitArtifacts(cfg); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		return 1
	}
	return 0
}

func emitArtifacts(cfg Config) error {
	manifest := provenance.ArtifactManifest{
		Artifacts: make([]provenance.ArtifactRecord, 0, len(cfg.OutputNames)),
	}
	for _, outputName := range cfg.OutputNames {
		outputName = strings.TrimSpace(outputName)
		if outputName == "" {
			continue
		}
		path := filepath.Join(cfg.OutputRoot, outputName)
		record, ok, err := buildArtifactRecord(cfg.RunID, cfg.NodeID, outputName, path)
		if err != nil {
			return err
		}
		if ok {
			manifest.Artifacts = append(manifest.Artifacts, record)
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal artifacts manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.ManifestPath), 0o750); err != nil {
		return fmt.Errorf("mkdir manifest dir: %w", err)
	}
	if err := os.WriteFile(cfg.ManifestPath, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if cfg.TerminationLogPath != "" {
		_ = os.WriteFile(cfg.TerminationLogPath, raw, 0o600)
	}
	return nil
}

func buildArtifactRecord(runID, nodeID, outputName, path string) (provenance.ArtifactRecord, bool, error) {
	path, err := secureOutputPath(path)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("sanitize output %s: %w", outputName, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return provenance.ArtifactRecord{}, false, nil
		}
		return provenance.ArtifactRecord{}, false, fmt.Errorf("stat output %s: %w", outputName, err)
	}
	if !info.Mode().IsRegular() {
		return provenance.ArtifactRecord{}, false, nil
	}
	// #nosec G304 -- path is normalized and must resolve under the container output root.
	f, err := os.Open(path)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("open output %s: %w", outputName, err)
	}
	defer func() {
		_ = f.Close()
	}()

	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("hash output %s: %w", outputName, err)
	}

	return provenance.ArtifactRecord{
		OutputName: outputName,
		URI:        fmt.Sprintf("jumi://runs/%s/nodes/%s/outputs/%s", runID, nodeID, outputName),
		Digest:     "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		SizeBytes:  size,
	}, true, nil
}

func secureOutputPath(path string) (string, error) {
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("invalid output path %q", path)
	}
	return cleaned, nil
}

func ParseOutputNames(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func stdoutOrDefault(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stdout
}

func stderrOrDefault(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stderr
}
