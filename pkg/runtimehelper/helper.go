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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/contract"
	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
)

const Version = "v0.1.0"

var (
	errInvalidConfig         = errors.New("invalid config")
	errInvalidOutputPath     = errors.New("invalid output path")
	errMissingRequiredOutput = errors.New("missing required output")
	errUnsupportedOutputType = errors.New("unsupported output type")
	errInspectFailed         = errors.New("inspect failed")
	errManifestWriteFailed   = errors.New("manifest write failed")
)

const (
	ExitSuccess               = 0
	ExitGenericError          = 1
	ExitInvalidConfig         = 2
	ExitInvalidCommand        = 64
	ExitInvalidOutputPath     = 65
	ExitMissingRequiredOutput = 66
	ExitUnsupportedOutputType = 67
	ExitInspectFailed         = 70
	ExitManifestWriteFailed   = 74
)

type OutputSpec struct {
	Name     string
	Path     string
	Required bool
	Type     string
}

// Config describes the runtime-side artifact helper contract executed inside
// the DAG node runtime container after wrapping the user command.
type Config struct {
	RunID                string
	SampleRunID          string
	NodeID               string
	AttemptID            string
	ContainerName        string
	OutputNames          []string
	Outputs              []OutputSpec
	OutputRoot           string
	ManifestPath         string
	TerminationLogPath   string
	AllowDirectoryOutput bool
	Command              []string
	Stdout               io.Writer
	Stderr               io.Writer
}

func (c Config) Validate() error {
	if c.RunID == "" {
		return fmt.Errorf("%w: runID is required", errInvalidConfig)
	}
	if c.NodeID == "" {
		return fmt.Errorf("%w: nodeID is required", errInvalidConfig)
	}
	if c.OutputRoot == "" {
		return fmt.Errorf("%w: outputRoot is required", errInvalidConfig)
	}
	if c.ManifestPath == "" {
		return fmt.Errorf("%w: manifestPath is required", errInvalidConfig)
	}
	return nil
}

func Run(ctx context.Context, cfg Config) int {
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		return ExitInvalidConfig
	}
	if len(cfg.Command) == 0 {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), "invalid config: command is required")
		return ExitInvalidCommand
	}

	if err := executeCommand(ctx, cfg); err != nil {
		return exitCode(err)
	}

	if err := EmitArtifacts(cfg); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		return classifyHelperError(err)
	}
	return ExitSuccess
}

func Inspect(cfg Config) int {
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		return ExitInvalidConfig
	}
	if err := EmitArtifacts(cfg); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		return classifyHelperError(err)
	}
	return ExitSuccess
}

func EmitArtifacts(cfg Config) error {
	outputs := cfg.effectiveOutputs()
	manifest := provenance.ArtifactManifest{
		SchemaVersion: provenance.ArtifactManifestSchemaVersion,
		RunID:         cfg.RunID,
		SampleRunID:   cfg.SampleRunID,
		NodeID:        cfg.NodeID,
		AttemptID:     cfg.AttemptID,
		ContainerName: cfg.ContainerName,
		NanVersion:    Version,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		OutputRoot:    cfg.OutputRoot,
		Artifacts:     make([]provenance.ArtifactRecord, 0, len(outputs)),
	}
	for _, output := range outputs {
		record, ok, err := buildArtifactRecord(cfg, output)
		if err != nil {
			return err
		}
		if ok {
			manifest.Artifacts = append(manifest.Artifacts, record)
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("%w: marshal artifacts manifest: %v", errManifestWriteFailed, err)
	}
	if err := atomicWriteFile(cfg.ManifestPath, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("%w: write manifest: %v", errManifestWriteFailed, err)
	}
	if cfg.TerminationLogPath != "" {
		_ = atomicWriteFile(cfg.TerminationLogPath, raw, 0o600)
	}
	return nil
}

func buildArtifactRecord(cfg Config, output OutputSpec) (provenance.ArtifactRecord, bool, error) {
	path, err := secureOutputPath(cfg.OutputRoot, output.Path)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s: %v", errInvalidOutputPath, output.Name, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if output.Required {
				return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s missing", errMissingRequiredOutput, output.Name)
			}
			return provenance.ArtifactRecord{}, false, nil
		}
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: stat output %s: %v", errInspectFailed, output.Name, err)
	}
	if !info.Mode().IsRegular() {
		if info.IsDir() && !cfg.AllowDirectoryOutput {
			return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s is a directory", errUnsupportedOutputType, output.Name)
		}
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s is not a regular file", errUnsupportedOutputType, output.Name)
	}
	// #nosec G304 -- path is normalized and must resolve under the container output root.
	f, err := os.Open(path)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: open output %s: %v", errInspectFailed, output.Name, err)
	}
	defer func() {
		_ = f.Close()
	}()

	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: hash output %s: %v", errInspectFailed, output.Name, err)
	}

	uri := fmt.Sprintf("jumi://runs/%s/nodes/%s/outputs/%s", cfg.RunID, cfg.NodeID, output.Name)
	if cfg.AttemptID != "" {
		uri = fmt.Sprintf("jumi://runs/%s/nodes/%s/attempts/%s/outputs/%s", cfg.RunID, cfg.NodeID, cfg.AttemptID, output.Name)
	}
	return provenance.ArtifactRecord{
		OutputName:   output.Name,
		DeclaredPath: output.Path,
		AbsolutePath: path,
		URI:          uri,
		Digest:       "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		SizeBytes:    size,
		Type:         firstNonEmpty(output.Type, "file"),
	}, true, nil
}

func secureOutputPath(outputRoot, declaredPath string) (string, error) {
	if declaredPath == "" {
		return "", fmt.Errorf("empty output path")
	}
	if filepath.IsAbs(declaredPath) {
		return "", fmt.Errorf("absolute output path is not allowed")
	}
	if !filepath.IsLocal(declaredPath) {
		return "", fmt.Errorf("non-local output path is not allowed")
	}
	candidate := filepath.Clean(filepath.Join(outputRoot, declaredPath))
	rel, err := filepath.Rel(outputRoot, candidate)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("output path escapes output root")
	}
	return candidate, nil
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
		return ExitSuccess
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return ExitGenericError
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

func executeCommand(ctx context.Context, cfg Config) error {
	// #nosec G204 -- runtimehelper intentionally executes the node command selected by the run spec.
	cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
	cmd.Stdout = stdoutOrDefault(cfg.Stdout)
	cmd.Stderr = stderrOrDefault(cfg.Stderr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 4)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for {
			select {
			case sig := <-sigCh:
				forwardSignal(cmd, sig)
			case <-stopCh:
				return
			}
		}
	}()

	err := cmd.Wait()
	close(stopCh)
	signal.Stop(sigCh)
	return err
}

func forwardSignal(cmd *exec.Cmd, sig os.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if sigv, ok := sig.(syscall.Signal); ok {
		if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil && pgid > 0 {
			_ = syscall.Kill(-pgid, sigv)
			return
		}
	}
	_ = cmd.Process.Signal(sig)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func classifyHelperError(err error) int {
	switch {
	case errors.Is(err, errInvalidConfig):
		return ExitInvalidConfig
	case errors.Is(err, errInvalidOutputPath):
		return ExitInvalidOutputPath
	case errors.Is(err, errMissingRequiredOutput):
		return ExitMissingRequiredOutput
	case errors.Is(err, errUnsupportedOutputType):
		return ExitUnsupportedOutputType
	case errors.Is(err, errManifestWriteFailed):
		return ExitManifestWriteFailed
	case errors.Is(err, errInspectFailed):
		return ExitInspectFailed
	default:
		return ExitGenericError
	}
}

func (c Config) effectiveOutputs() []OutputSpec {
	if len(c.Outputs) != 0 {
		return c.Outputs
	}
	outputs := make([]OutputSpec, 0, len(c.OutputNames))
	for _, name := range c.OutputNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		outputs = append(outputs, OutputSpec{
			Name:     name,
			Path:     name,
			Required: true,
			Type:     "file",
		})
	}
	return outputs
}

func ConfigFromContract(c contract.NodeContract) Config {
	outputs := make([]OutputSpec, 0, len(c.Outputs))
	for _, output := range c.Outputs {
		required := output.Required
		if !output.Required && c.Runtime.FailOnMissingRequiredOutput && output.Type == "" {
			required = false
		}
		outputs = append(outputs, OutputSpec{
			Name:     output.Name,
			Path:     output.Path,
			Required: required,
			Type:     firstNonEmpty(output.Type, "file"),
		})
	}
	return Config{
		RunID:                c.RunID,
		SampleRunID:          c.SampleRunID,
		NodeID:               c.NodeID,
		AttemptID:            c.AttemptID,
		ContainerName:        c.ContainerName,
		Outputs:              outputs,
		OutputRoot:           c.Paths.OutputRoot,
		ManifestPath:         c.Paths.ManifestPath,
		AllowDirectoryOutput: c.Runtime.AllowDirectoryOutput,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
