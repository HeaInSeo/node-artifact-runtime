package runtimehelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HeaInSeo/node-artifact-runtime/pkg/contract"
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
	errProcessGroupNotClean  = errors.New("process group not clean")
	errSubreaperSetupFailed  = errors.New("subreaper setup failed")
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

const DefaultShutdownGracePeriod = 25 * time.Second

const (
	linuxPRSetChildSubreaper = 36
	processGroupPollInterval = 10 * time.Millisecond
	processGroupKillWait     = 2 * time.Second
)

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

	lifecycle := newRunLifecycle(ctx, cfg)
	defer lifecycle.stop()
	ctx = lifecycle.ctx

	if err := MaterializeInputs(ctx, cfg); err != nil {
		if ctx.Err() != nil {
			return writeLifecycleCancellationSummary(cfg, lifecycle)
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

	result := executeCommand(ctx, cfg, lifecycle)
	if result.TimedOut {
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
	if result.Interrupted {
		status := "interrupted"
		if result.Killed {
			status = "killed"
		}
		msg := fmt.Sprintf("run interrupted by %s", result.Signal)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    status,
			ExitCode:  result.ExitCode,
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   msg,
		})
		return result.ExitCode
	}
	if result.Err != nil {
		status := "command_failed"
		if result.NotClean {
			status = "process_group_not_clean"
		}
		code := result.ExitCode
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    status,
			ExitCode:  code,
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   result.Err.Error(),
		})
		return code
	}

	if err := EmitArtifactsContext(ctx, cfg); err != nil {
		if ctx.Err() != nil {
			return writeLifecycleCancellationSummary(cfg, lifecycle)
		}
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

func writeLifecycleCancellationSummary(cfg Config, lifecycle *runLifecycle) int {
	if sigv, ok := lifecycle.interruptedSignal(); ok {
		msg := fmt.Sprintf("run interrupted by %s", sigv)
		writeTerminationSummary(cfg, TerminationSummary{
			Status:    "interrupted",
			ExitCode:  signalExitCode(sigv),
			RunID:     cfg.RunID,
			NodeID:    cfg.NodeID,
			AttemptID: cfg.AttemptID,
			Message:   msg,
		})
		return signalExitCode(sigv)
	}
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
		if looksLikeSignedURLQuery(parsed.RawQuery) {
			return fmt.Errorf("signed URL query string is not allowed; use runtime-only credentialRef flow")
		}
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

func looksLikeSignedURLQuery(rawQuery string) bool {
	rawQuery = strings.ToLower(rawQuery)
	for _, marker := range []string{
		"x-amz-signature",
		"x-amz-credential",
		"x-goog-signature",
		"x-goog-credential",
		"x-ms-signature",
		"signature=",
		"expires=",
	} {
		if strings.Contains(rawQuery, marker) {
			return true
		}
	}
	return false
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
