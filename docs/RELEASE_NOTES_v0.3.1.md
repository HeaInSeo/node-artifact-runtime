# nan v0.3.1 Release Notes

Date: 2026-07-02

## Summary

`v0.3.1` hardens `nan` as a PID 1-capable runtime shim for JUMI DAG node
containers and refactors the runtime helper package into reviewable boundaries.

## Runtime Changes

- Stores the child process group ID immediately after command start.
- Forwards shutdown signals to the stored process group instead of resolving it
  from a potentially exited process leader.
- Treats leftover process-group members after direct child exit as failure.
- Suppresses success artifact manifest emission after timeout or external
  interruption.
- Extends lifecycle signal handling beyond command execution into materialize
  and artifact inspection phases.
- Adds context-aware manifest writes so timeout/interruption can stop before
  publishing the final success manifest.
- Aligns the reported `nan` version with the release tag: `v0.3.1`.

## Code Organization

- `run.go`: run lifecycle orchestration.
- `inspect.go`: inspect lifecycle and output-name parsing.
- `supervisor.go` / `supervisor_linux.go`: process supervision and Linux
  process-group primitives.
- `materialize.go`: remote fetch and local reuse input materialization.
- `outputs.go`, `cas.go`, `manifest.go`, `paths.go`: artifact inspection,
  CAS promotion, manifest writes, and path safety.
- `contract.go`, `env.go`: JUMI contract and environment conversion.

## Validation

Required validation for this release:

```text
go test ./...
go test -race ./pkg/runtimehelper
scripts/nan-pid1-container-smoke.sh
```

The container smoke test requires a working local container engine. It verifies
that `nan` can run as PID 1, receives SIGTERM, exits with signal-style status,
and does not publish a success manifest during shutdown.

## Known Policy Gap

Directory outputs are explicitly rejected for production correctness until a
deterministic tree manifest with sorted entries and per-file digests is
implemented.

Tracking issue: https://github.com/HeaInSeo/node-artifact-runtime/issues/1
