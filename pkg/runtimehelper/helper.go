package runtimehelper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
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
