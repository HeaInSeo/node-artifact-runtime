package runtimehelper

import (
	"context"
	"fmt"
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
