package runtimehelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/contract"
	"github.com/HeaInSeo/node-artifact-runtime/pkg/provenance"
)

const Version = "v0.1.5"

var (
	errInvalidConfig         = errors.New("invalid config")
	errInvalidOutputPath     = errors.New("invalid output path")
	errMissingRequiredOutput = errors.New("missing required output")
	errUnsupportedOutputType = errors.New("unsupported output type")
	errInspectFailed         = errors.New("inspect failed")
	errMaterializeFailed     = errors.New("materialize failed")
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
	ExitMaterializeFailed     = 69
	ExitInspectFailed         = 70
	ExitManifestWriteFailed   = 74
	ExitTimeout               = 75
)

type OutputSpec struct {
	Name     string
	Path     string
	Required bool
	Type     string
}

type InputSpec struct {
	Name                string
	URI                 string
	ExpectedDigest      string
	ExpectedSizeBytes   int64
	MaterializationMode string
	NodeLocalPath       string
	LocalPath           string
}

type partialInputSpec struct {
	name                string
	uri                 string
	expectedDigest      string
	expectedSizeBytes   string
	materializationMode string
	nodeLocalPath       string
	localPath           string
}

type TerminationSummary struct {
	Status       string `json:"status"`
	ExitCode     int    `json:"exitCode"`
	RunID        string `json:"runId,omitempty"`
	NodeID       string `json:"nodeId,omitempty"`
	AttemptID    string `json:"attemptId,omitempty"`
	ManifestPath string `json:"manifestPath,omitempty"`
	Message      string `json:"message,omitempty"`
}

// Config describes the runtime-side artifact helper contract executed inside
// the DAG node runtime container after wrapping the user command.
type Config struct {
	RunID                 string
	SampleRunID           string
	NodeID                string
	AttemptID             string
	ContainerName         string
	OutputNames           []string
	Inputs                []InputSpec
	WorkRoot              string
	NodeLocalArtifactRoot string
	HTTPAllowedHosts      []string
	HTTPAllowAny          bool
	HTTPTimeout           time.Duration
	HTTPMaxRedirects      int
	HTTPMaxInputBytes     int64
	Outputs               []OutputSpec
	OutputRoot            string
	ManifestPath          string
	TerminationLogPath    string
	AllowDirectoryOutput  bool
	InspectOnSuccessOnly  bool
	// CommandExitCode holds the exit code of the user command when Inspect is
	// called standalone (not via Run). Used with InspectOnSuccessOnly.
	CommandExitCode int
	// RunTimeout is a wall-clock limit for the entire Run lifecycle
	// (materialize + execute + inspect). Zero means no timeout.
	RunTimeout time.Duration
	Command    []string
	Stdout     io.Writer
	Stderr     io.Writer
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
	for _, input := range c.Inputs {
		if input.ExpectedSizeBytes < 0 {
			return fmt.Errorf("%w: input %s expected size must be >= 0", errInvalidConfig, input.Name)
		}
	}
	return nil
}

func Run(ctx context.Context, cfg Config) int {
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    "invalid_config",
			ExitCode:  ExitInvalidConfig,
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   err.Error(),
		})
		return ExitInvalidConfig
	}
	if len(cfg.Command) == 0 {
		msg := "invalid config: command is required"
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), msg)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    "invalid_command",
			ExitCode:  ExitInvalidCommand,
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   msg,
		})
		return ExitInvalidCommand
	}

	if cfg.RunTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.RunTimeout)
		defer cancel()
	}

	if err := MaterializeInputs(ctx, cfg); err != nil {
		if ctx.Err() != nil {
			msg := fmt.Sprintf("run timed out after %s", cfg.RunTimeout)
			writeTerminationSummary(cfg, TerminationSummary{
				Status:    "timeout",
				ExitCode:  ExitTimeout,
				RunID:     cfg.RunID,
				NodeID:    cfg.NodeID,
				AttemptID: cfg.AttemptID,
				Message:   msg,
			})
			return ExitTimeout
		}
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    "materialization_failed",
			ExitCode:  ExitMaterializeFailed,
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   err.Error(),
		})
		return ExitMaterializeFailed
	}

	if err := executeCommand(ctx, cfg); err != nil {
		if ctx.Err() != nil {
			msg := fmt.Sprintf("run timed out after %s", cfg.RunTimeout)
			writeTerminationSummary(cfg, TerminationSummary{
				Status:    "timeout",
				ExitCode:  ExitTimeout,
				RunID:     cfg.RunID,
				NodeID:    cfg.NodeID,
				AttemptID: cfg.AttemptID,
				Message:   msg,
			})
			return ExitTimeout
		}
		code := exitCode(err)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    "command_failed",
			ExitCode:  code,
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   err.Error(),
		})
		return code
	}

	if err := EmitArtifacts(cfg); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		code := classifyHelperError(err)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:       "inspect_failed",
			ExitCode:     code,
			RunID:        cfg.RunID,
			NodeID:       cfg.NodeID,
			AttemptID:    cfg.AttemptID,
			ManifestPath: cfg.ManifestPath,
			Message:      err.Error(),
		})
		return code
	}
	writeTerminationManifest(cfg)
	return ExitSuccess
}

func Inspect(cfg Config) int {
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    "invalid_config",
			ExitCode:  ExitInvalidConfig,
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   err.Error(),
		})
		return ExitInvalidConfig
	}
	if cfg.InspectOnSuccessOnly && cfg.CommandExitCode != 0 {
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    "command_failed",
			ExitCode:  cfg.CommandExitCode,
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   fmt.Sprintf("inspect skipped: command exited %d", cfg.CommandExitCode),
		})
		return cfg.CommandExitCode
	}
	if err := EmitArtifacts(cfg); err != nil {
		_, _ = fmt.Fprintln(stderrOrDefault(cfg.Stderr), err)
		code := classifyHelperError(err)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:       "inspect_failed",
			ExitCode:     code,
			RunID:        cfg.RunID,
			NodeID:       cfg.NodeID,
			AttemptID:    cfg.AttemptID,
			ManifestPath: cfg.ManifestPath,
			Message:      err.Error(),
		})
		return code
	}
	writeTerminationManifest(cfg)
	return ExitSuccess
}

func MaterializeInputs(ctx context.Context, cfg Config) error {
	for _, input := range cfg.Inputs {
		switch strings.TrimSpace(input.MaterializationMode) {
		case "", "none":
			continue
		case "remote_fetch":
			if err := materializeRemoteFetchInput(ctx, cfg, input); err != nil {
				return err
			}
		case "local_reuse":
			if err := materializeLocalReuseInput(ctx, cfg, input); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: input %s has unsupported materialization mode %q", errMaterializeFailed, input.Name, input.MaterializationMode)
		}
	}
	return nil
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
	// Re-validate after symlink resolution: the declared path may be a symlink
	// whose target resolves outside the output root.
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: stat output %s: %v", errInspectFailed, output.Name, err)
	}
	cleanRoot := filepath.Clean(cfg.OutputRoot)
	rel, err := filepath.Rel(cleanRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s symlink escapes output root", errInvalidOutputPath, output.Name)
	}
	path = realPath
	if !info.Mode().IsRegular() {
		if info.IsDir() {
			if !cfg.AllowDirectoryOutput {
				return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s is a directory", errUnsupportedOutputType, output.Name)
			}
			// Directory output allowed; downstream CAS hashing does not support
			// directories yet and will return its own error.
		} else {
			return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s is not a regular file", errUnsupportedOutputType, output.Name)
		}
	}
	uri := fmt.Sprintf("jumi://runs/%s/nodes/%s/outputs/%s", cfg.RunID, cfg.NodeID, output.Name)
	if cfg.AttemptID != "" {
		uri = fmt.Sprintf("jumi://runs/%s/nodes/%s/attempts/%s/outputs/%s", cfg.RunID, cfg.NodeID, cfg.AttemptID, output.Name)
	}
	logicalURI := fmt.Sprintf("jumi://runs/%s/nodes/%s/outputs/%s", cfg.RunID, cfg.NodeID, output.Name)

	var (
		digest   string
		size     int64
		location *provenance.NodeLocalLocation
	)
	// Re-resolve symlinks immediately before opening to shrink the TOCTOU window.
	// If the resolved path changed since the earlier check, a symlink was swapped
	// between validation and use; reject to prevent escape.
	if recheckPath, rerr := filepath.EvalSymlinks(path); rerr != nil || recheckPath != path {
		return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: output %s path changed between validation and open (possible TOCTOU)", errInvalidOutputPath, output.Name)
	}
	if strings.TrimSpace(cfg.NodeLocalArtifactRoot) != "" {
		location, digest, size, err = promoteOutputToNodeLocalCAS(cfg, output, path)
		if err != nil {
			return provenance.ArtifactRecord{}, false, err
		}
	} else {
		// #nosec G304 -- path is validated above and resolves under the container output root.
		f, err := os.Open(path)
		if err != nil {
			return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: open output %s: %v", errInspectFailed, output.Name, err)
		}
		defer func() {
			_ = f.Close()
		}()
		hash := sha256.New()
		size, err = io.Copy(hash, f)
		if err != nil {
			return provenance.ArtifactRecord{}, false, fmt.Errorf("%w: hash output %s: %v", errInspectFailed, output.Name, err)
		}
		digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	}

	record := provenance.ArtifactRecord{
		OutputName:        output.Name,
		DeclaredPath:      output.Path,
		AbsolutePath:      path,
		URI:               uri,
		LogicalURI:        logicalURI,
		Digest:            digest,
		SizeBytes:         size,
		Type:              FirstNonEmpty(output.Type, "file"),
		ProducerAttemptID: cfg.AttemptID,
	}
	if location != nil {
		record.Locations = []provenance.ArtifactLocation{{NodeLocal: location}}
	}
	return record, true, nil
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
	cmdEnv, err := cfg.commandEnv()
	if err != nil {
		return err
	}
	cmd.Env = cmdEnv

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

	err = cmd.Wait()
	close(stopCh)
	signal.Stop(sigCh)
	return err
}

func materializeRemoteFetchInput(ctx context.Context, cfg Config, input InputSpec) error {
	if strings.TrimSpace(input.URI) == "" {
		return fmt.Errorf("%w: input %s has empty uri", errMaterializeFailed, input.Name)
	}
	if strings.TrimSpace(input.ExpectedDigest) == "" {
		return fmt.Errorf("%w: input %s has empty expected digest", errMaterializeFailed, input.Name)
	}
	if err := validateRemoteFetchURI(cfg, input.URI); err != nil {
		return fmt.Errorf("%w: input %s uri rejected: %v", errMaterializeFailed, input.Name, err)
	}
	workRoot := effectiveWorkRoot(cfg.WorkRoot)
	targetPath, err := materializedInputPath(workRoot, input)
	if err != nil {
		return fmt.Errorf("%w: input %s target path: %v", errMaterializeFailed, input.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir target dir for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := os.MkdirAll(filepath.Join(workRoot, ".jumi-fetch"), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir fetch temp dir for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	tmpFile, err := os.CreateTemp(filepath.Join(workRoot, ".jumi-fetch"), safeInputName(input.Name)+".*.part")
	if err != nil {
		return fmt.Errorf("%w: create temp file for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	tmpName := tmpFile.Name()
	defer func() { _ = os.Remove(tmpName) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URI, nil)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: create request for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	resp, err := remoteFetchClient(cfg).Do(req)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: fetch input %s: %v", errMaterializeFailed, input.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: fetch input %s returned status %d", errMaterializeFailed, input.Name, resp.StatusCode)
	}
	if maxBytes := effectiveHTTPMaxInputBytes(cfg, input); maxBytes > 0 && resp.ContentLength > maxBytes {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: input %s content-length %d exceeds limit %d", errMaterializeFailed, input.Name, resp.ContentLength, maxBytes)
	}

	hash := sha256.New()
	limit := effectiveHTTPMaxInputBytes(cfg, input)
	var bodyReader io.Reader = resp.Body
	if limit > 0 {
		bodyReader = io.LimitReader(resp.Body, limit+1)
	}
	written, err := io.Copy(io.MultiWriter(tmpFile, hash), bodyReader)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: read input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if limit > 0 && written > limit {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: input %s exceeds size limit %d", errMaterializeFailed, input.Name, limit)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: sync input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("%w: close input %s temp file: %v", errMaterializeFailed, input.Name, err)
	}

	actualDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actualDigest != strings.TrimSpace(input.ExpectedDigest) {
		return fmt.Errorf("%w: input %s digest mismatch: got %s want %s", errMaterializeFailed, input.Name, actualDigest, input.ExpectedDigest)
	}
	if input.ExpectedSizeBytes > 0 && written != input.ExpectedSizeBytes {
		return fmt.Errorf("%w: input %s size mismatch: got %d want %d", errMaterializeFailed, input.Name, written, input.ExpectedSizeBytes)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("%w: move input %s into place: %v", errMaterializeFailed, input.Name, err)
	}
	return nil
}

func materializeLocalReuseInput(_ context.Context, cfg Config, input InputSpec) error {
	if strings.TrimSpace(input.NodeLocalPath) == "" {
		return fmt.Errorf("%w: input %s has empty node-local path", errMaterializeFailed, input.Name)
	}
	if strings.TrimSpace(input.ExpectedDigest) == "" {
		return fmt.Errorf("%w: input %s has empty expected digest", errMaterializeFailed, input.Name)
	}
	if err := validateNodeLocalSourcePath(cfg, input.NodeLocalPath); err != nil {
		return fmt.Errorf("%w: input %s: %v", errMaterializeFailed, input.Name, err)
	}
	workRoot := effectiveWorkRoot(cfg.WorkRoot)
	targetPath, err := materializedInputPath(workRoot, input)
	if err != nil {
		return fmt.Errorf("%w: input %s target path: %v", errMaterializeFailed, input.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir target dir for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := os.MkdirAll(filepath.Join(workRoot, ".jumi-fetch"), 0o750); err != nil {
		return fmt.Errorf("%w: mkdir materialize temp dir for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	tmpFile, err := os.CreateTemp(filepath.Join(workRoot, ".jumi-fetch"), safeInputName(input.Name)+".*.part")
	if err != nil {
		return fmt.Errorf("%w: create temp file for input %s: %v", errMaterializeFailed, input.Name, err)
	}
	tmpName := tmpFile.Name()
	defer func() { _ = os.Remove(tmpName) }()

	sourceFile, err := os.Open(input.NodeLocalPath)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: open node-local input %s: %v", errMaterializeFailed, input.Name, err)
	}
	defer func() { _ = sourceFile.Close() }()

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, hash), sourceFile)
	if err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: copy node-local input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("%w: sync input %s: %v", errMaterializeFailed, input.Name, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("%w: close input %s temp file: %v", errMaterializeFailed, input.Name, err)
	}

	actualDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actualDigest != strings.TrimSpace(input.ExpectedDigest) {
		return fmt.Errorf("%w: input %s digest mismatch: got %s want %s", errMaterializeFailed, input.Name, actualDigest, input.ExpectedDigest)
	}
	if input.ExpectedSizeBytes > 0 && written != input.ExpectedSizeBytes {
		return fmt.Errorf("%w: input %s size mismatch: got %d want %d", errMaterializeFailed, input.Name, written, input.ExpectedSizeBytes)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("%w: move input %s into place: %v", errMaterializeFailed, input.Name, err)
	}
	return nil
}

func validateNodeLocalSourcePath(cfg Config, sourcePath string) error {
	if !filepath.IsAbs(sourcePath) {
		return fmt.Errorf("node-local source path must be absolute: %q", sourcePath)
	}
	root := strings.TrimSpace(cfg.NodeLocalArtifactRoot)
	if root == "" {
		return fmt.Errorf("node-local artifact root is required for local_reuse")
	}
	cleanedRoot := filepath.Clean(root)
	cleanedPath := filepath.Clean(sourcePath)
	rel, err := filepath.Rel(cleanedRoot, cleanedPath)
	if err != nil {
		return err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("node-local source path %q is outside allowed root %q", sourcePath, cleanedRoot)
	}
	info, err := os.Lstat(cleanedPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("node-local source path %q must not be a symlink", sourcePath)
	}
	realRoot, err := filepath.EvalSymlinks(cleanedRoot)
	if err != nil {
		return fmt.Errorf("resolve node-local artifact root: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(cleanedPath)
	if err != nil {
		return fmt.Errorf("resolve node-local source path: %w", err)
	}
	realRel, err := filepath.Rel(realRoot, realPath)
	if err != nil {
		return err
	}
	if realRel == "." || realRel == ".." || strings.HasPrefix(realRel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("node-local source path %q escapes resolved root %q", sourcePath, realRoot)
	}
	return nil
}

func materializedInputPath(workRoot string, input InputSpec) (string, error) {
	workRoot = effectiveWorkRoot(workRoot)
	if strings.TrimSpace(input.LocalPath) != "" {
		return secureInputMaterializedPath(workRoot, input.LocalPath)
	}
	return secureInputMaterializedPath(workRoot, filepath.Join("inputs", strings.ToLower(safeInputName(input.Name))))
}

func validateRemoteFetchURI(cfg Config, rawURI string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return err
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("credential-bearing userinfo is not allowed")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf("query string is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
			return fmt.Errorf("host %q is not allowed", host)
		}
	}
	if len(cfg.HTTPAllowedHosts) != 0 {
		allowed := false
		for _, candidate := range cfg.HTTPAllowedHosts {
			if strings.EqualFold(strings.TrimSpace(candidate), host) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("host %q is not in allowlist", host)
		}
	} else if !cfg.HTTPAllowAny {
		return fmt.Errorf("http source allowlist is required")
	}
	return nil
}

func remoteFetchClient(cfg Config) *http.Client {
	maxRedirects := cfg.HTTPMaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 3
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	transport.ResponseHeaderTimeout = timeout
	transport.IdleConnTimeout = 30 * time.Second
	// Re-validate the resolved IP at dial time to block DNS rebinding attacks:
	// an attacker-controlled DNS server may return a public IP at validation
	// time and a private IP when the actual connection is made.
	// Hosts listed in HTTPAllowedHosts are explicitly trusted and skip the
	// resolved-IP check (allows test servers on loopback).
	dialer := &net.Dialer{}
	allowedHosts := cfg.HTTPAllowedHosts
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		for _, a := range allowedHosts {
			if strings.EqualFold(strings.TrimSpace(a), host) {
				return dialer.DialContext(ctx, network, addr)
			}
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses resolved for host %q", host)
		}
		for _, a := range ips {
			if a.IP.IsLoopback() || a.IP.IsLinkLocalUnicast() || a.IP.IsLinkLocalMulticast() || a.IP.IsPrivate() {
				return nil, fmt.Errorf("host %q resolved to disallowed address %s", host, a.IP)
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
				return fmt.Errorf("redirect scheme downgrade is not allowed")
			}
			return validateRemoteFetchURI(cfg, req.URL.String())
		},
	}
}

func effectiveHTTPMaxInputBytes(cfg Config, input InputSpec) int64 {
	if input.ExpectedSizeBytes > 0 {
		return input.ExpectedSizeBytes
	}
	return cfg.HTTPMaxInputBytes
}

func secureInputMaterializedPath(workRoot, relativePath string) (string, error) {
	if relativePath == "" {
		return "", fmt.Errorf("empty materialized path")
	}
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("absolute materialized path is not allowed")
	}
	if !filepath.IsLocal(relativePath) {
		return "", fmt.Errorf("non-local materialized path is not allowed")
	}
	candidate := filepath.Clean(filepath.Join(workRoot, relativePath))
	rel, err := filepath.Rel(workRoot, candidate)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("materialized path escapes work root")
	}
	inputsPrefix := "inputs" + string(os.PathSeparator)
	if rel == "inputs" || !strings.HasPrefix(rel, inputsPrefix) {
		return "", fmt.Errorf("materialized path must stay under inputs/")
	}
	if err := ensureNoSymlinkPathPrefix(workRoot, rel); err != nil {
		return "", err
	}
	return candidate, nil
}

func ensureNoSymlinkPathPrefix(workRoot, relativePath string) error {
	current := filepath.Clean(workRoot)
	parts := strings.Split(relativePath, string(os.PathSeparator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				if i == len(parts)-1 {
					return nil
				}
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("materialized path traverses symlink %q", current)
		}
	}
	return nil
}

func safeInputName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "input"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "input"
	}
	const maxLen = 64
	if len(out) > maxLen {
		out = strings.TrimRight(out[:maxLen], "-")
		if out == "" {
			return "input"
		}
	}
	return out
}

func effectiveWorkRoot(workRoot string) string {
	return FirstNonEmpty(workRoot, "/work")
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
	case errors.Is(err, errMaterializeFailed):
		return ExitMaterializeFailed
	case errors.Is(err, errManifestWriteFailed):
		return ExitManifestWriteFailed
	case errors.Is(err, errInspectFailed):
		return ExitInspectFailed
	default:
		return ExitGenericError
	}
}

func promoteOutputToNodeLocalCAS(cfg Config, output OutputSpec, sourcePath string) (*provenance.NodeLocalLocation, string, int64, error) {
	root := filepath.Clean(cfg.NodeLocalArtifactRoot)
	casDir := filepath.Join(root, "cas", "sha256")
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(casDir, 0o750); err != nil {
		return nil, "", 0, fmt.Errorf("%w: mkdir CAS dir for output %s: %v", errInspectFailed, output.Name, err)
	}
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, "", 0, fmt.Errorf("%w: mkdir tmp dir for output %s: %v", errInspectFailed, output.Name, err)
	}
	tmpFile, err := os.CreateTemp(tmpDir, fmt.Sprintf("%s-%s-%s-%s-", cfg.RunID, cfg.NodeID, cfg.AttemptID, output.Name))
	if err != nil {
		return nil, "", 0, fmt.Errorf("%w: create temp CAS file for output %s: %v", errInspectFailed, output.Name, err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		_ = tmpFile.Close()
		return nil, "", 0, fmt.Errorf("%w: open output %s for CAS promotion: %v", errInspectFailed, output.Name, err)
	}
	defer func() {
		_ = sourceFile.Close()
	}()

	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmpFile, hash), sourceFile)
	if err != nil {
		_ = tmpFile.Close()
		return nil, "", 0, fmt.Errorf("%w: copy output %s to temp CAS: %v", errInspectFailed, output.Name, err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return nil, "", 0, fmt.Errorf("%w: sync temp CAS file for output %s: %v", errInspectFailed, output.Name, err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, "", 0, fmt.Errorf("%w: close temp CAS file for output %s: %v", errInspectFailed, output.Name, err)
	}

	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if hexDigest == "" || hexDigest == digest {
		return nil, "", 0, fmt.Errorf("%w: output %s has unsupported digest %q", errInspectFailed, output.Name, digest)
	}
	finalPath := filepath.Join(casDir, hexDigest)
	if ok, err := verifyExistingCASArtifact(finalPath, digest); err != nil {
		return nil, "", 0, err
	} else if ok {
		return &provenance.NodeLocalLocation{Path: finalPath}, digest, size, nil
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return nil, "", 0, fmt.Errorf("%w: move promoted output %s into CAS: %v", errInspectFailed, output.Name, err)
	}
	return &provenance.NodeLocalLocation{Path: finalPath}, digest, size, nil
}

func verifyExistingCASArtifact(path, expectedDigest string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("%w: stat existing CAS artifact: %v", errInspectFailed, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: existing CAS artifact is not a regular file", errInspectFailed)
	}
	actual, err := sha256Digest(path)
	if err != nil {
		return false, fmt.Errorf("%w: hash existing CAS artifact: %v", errInspectFailed, err)
	}
	if actual != expectedDigest {
		return false, fmt.Errorf("%w: existing CAS artifact digest mismatch: got %s want %s", errInspectFailed, actual, expectedDigest)
	}
	return true, nil
}

func sha256Digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
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
	inputs := make([]InputSpec, 0, len(c.Inputs))
	for _, input := range c.Inputs {
		inputs = append(inputs, InputSpec{
			Name:                input.Name,
			URI:                 input.URI,
			ExpectedDigest:      input.ExpectedDigest,
			ExpectedSizeBytes:   input.ExpectedSizeBytes,
			MaterializationMode: input.MaterializationMode,
			NodeLocalPath:       input.NodeLocalPath,
			LocalPath:           input.LocalPath,
		})
	}
	outputs := make([]OutputSpec, 0, len(c.Outputs))
	for _, output := range c.Outputs {
		required := output.Required
		if !output.Required && c.Runtime.FailOnMissingRequiredOutput {
			required = true
		}
		outputs = append(outputs, OutputSpec{
			Name:     output.Name,
			Path:     output.Path,
			Required: required,
			Type:     FirstNonEmpty(output.Type, "file"),
		})
	}
	return Config{
		RunID:                c.RunID,
		SampleRunID:          c.SampleRunID,
		NodeID:               c.NodeID,
		AttemptID:            c.AttemptID,
		ContainerName:        c.ContainerName,
		Inputs:               inputs,
		Outputs:              outputs,
		WorkRoot:             FirstNonEmpty(c.Paths.WorkRoot, "/work"),
		OutputRoot:           c.Paths.OutputRoot,
		ManifestPath:         c.Paths.ManifestPath,
		AllowDirectoryOutput: c.Runtime.AllowDirectoryOutput,
		InspectOnSuccessOnly: c.Runtime.InspectOnSuccessOnly,
	}
}

func ParseInputSpecsFromEnv(env []string, workRoot string) ([]InputSpec, error) {
	byBase := map[string]*partialInputSpec{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, "JUMI_INPUT_") {
			continue
		}
		switch {
		case strings.HasSuffix(key, "_URI"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_URI")
			p := ensurePartial(byBase, base)
			p.uri = value
		case strings.HasSuffix(key, "_EXPECTED_DIGEST"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_EXPECTED_DIGEST")
			p := ensurePartial(byBase, base)
			p.expectedDigest = value
		case strings.HasSuffix(key, "_EXPECTED_SIZE_BYTES"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_EXPECTED_SIZE_BYTES")
			p := ensurePartial(byBase, base)
			p.expectedSizeBytes = value
		case strings.HasSuffix(key, "_MATERIALIZATION_MODE"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_MATERIALIZATION_MODE")
			p := ensurePartial(byBase, base)
			p.materializationMode = value
		case strings.HasSuffix(key, "_NODE_LOCAL_PATH"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_NODE_LOCAL_PATH")
			p := ensurePartial(byBase, base)
			p.nodeLocalPath = value
		case strings.HasSuffix(key, "_LOCAL_PATH") && !strings.HasSuffix(key, "_NODE_LOCAL_PATH"):
			base := strings.TrimSuffix(strings.TrimPrefix(key, "JUMI_INPUT_"), "_LOCAL_PATH")
			p := ensurePartial(byBase, base)
			p.localPath = value
		}
	}
	bases := make([]string, 0, len(byBase))
	for base := range byBase {
		bases = append(bases, base)
	}
	sort.Strings(bases)
	inputs := make([]InputSpec, 0, len(bases))
	for _, base := range bases {
		p := byBase[base]
		if strings.TrimSpace(p.materializationMode) == "" {
			continue
		}
		expectedSizeBytes := int64(0)
		if strings.TrimSpace(p.expectedSizeBytes) != "" {
			parsed, err := strconv.ParseInt(strings.TrimSpace(p.expectedSizeBytes), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid expected size for input %s: %v", errInvalidConfig, base, err)
			}
			if parsed <= 0 {
				return nil, fmt.Errorf("%w: invalid expected size for input %s: must be > 0", errInvalidConfig, base)
			}
			expectedSizeBytes = parsed
		}
		inputs = append(inputs, InputSpec{
			Name:                strings.ToLower(base),
			URI:                 p.uri,
			ExpectedDigest:      p.expectedDigest,
			ExpectedSizeBytes:   expectedSizeBytes,
			MaterializationMode: p.materializationMode,
			NodeLocalPath:       p.nodeLocalPath,
			LocalPath:           FirstNonEmpty(p.localPath, filepath.Join("inputs", strings.ToLower(base))),
		})
	}
	return inputs, nil
}

func ensurePartial(partials map[string]*partialInputSpec, base string) *partialInputSpec {
	if partials[base] == nil {
		partials[base] = &partialInputSpec{name: base}
	}
	return partials[base]
}

func (c Config) commandEnv() ([]string, error) {
	env := append([]string{}, os.Environ()...)
	for _, input := range c.Inputs {
		localPath, err := materializedInputPath(effectiveWorkRoot(c.WorkRoot), input)
		if err != nil {
			return nil, fmt.Errorf("env for input %s: %w", input.Name, err)
		}
		keyBase := strings.ToUpper(strings.ReplaceAll(safeInputName(input.Name), "-", "_"))
		env = append(env, "JUMI_INPUT_"+keyBase+"_LOCAL_PATH="+localPath)
	}
	return env, nil
}

// FirstNonEmpty returns the first non-empty string among values, or "" if all are empty.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
		fmt.Fprintf(stderrOrDefault(cfg.Stderr), "warning: failed to write termination log: %v\n", err)
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
		fmt.Fprintf(stderrOrDefault(cfg.Stderr), "warning: failed to write termination manifest: %v\n", err)
	}
}

func writeTerminationLog(path string, data []byte) error {
	if path == "" {
		return nil
	}
	return os.WriteFile(path, data, 0o600) //nolint:gosec // path is operator-controlled (/dev/termination-log default or explicit flag)
}
