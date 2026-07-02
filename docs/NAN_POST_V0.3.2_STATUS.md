# nan Post-v0.3.2 Status

Date: 2026-07-02

## Current State

`nan` is now a production-candidate runtime shim for the current JUMI file
artifact contract.

Completed:

- PID 1-oriented process supervision hardening.
- Full-run lifecycle signal context.
- Process-group residue fail-closed behavior.
- Timeout/interruption manifest suppression.
- Context-aware manifest write checks.
- Runtime helper package split into focused files.
- PID 1 container smoke script and CI workflow.
- Directory outputs explicitly fail fast instead of being partially supported.

## Supported Artifact Scope

Production-supported output artifact type:

- regular file

Explicitly unsupported:

- directory output artifact
- symlink escape from output root
- non-regular file output

Directory tree artifact support requires a deterministic tree manifest design
before it should be considered production-supported.

## Recommended Next Work

1. Keep issue #1 open for deterministic directory tree artifacts.
2. Pin JUMI to `node-artifact-runtime` `v0.3.2`.
3. Treat future supervisor or manifest behavior changes as patch releases only
   when the runtime contract remains backward compatible.
