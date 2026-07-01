# nan Commercial Readiness Review

Status: post-v0.3.0 static assessment

Reviewed target:

- repository: `node-artifact-runtime`
- executable: `nan`
- release baseline: `v0.3.0`

## Summary

Commercial-readiness score:

```text
6.8 / 10
```

`nan` is beyond a simple proof of concept and is close to an MVP runtime shim.
The product idea is strong and the implementation already shows attention to
artifact correctness, path safety, remote-fetch security, and Kubernetes
shutdown behavior.

It is not yet production-grade as a critical runtime shim. The main gaps are:

1. PID 1 process supervision is incomplete.
2. Artifact finalization still has edge cases around background descendants.
3. Runtime helper responsibilities are concentrated in a large monolithic file.

In short:

```text
POC: pass
MVP: nearly pass
Beta: possible with conditions
Production: not yet
Commercial critical path: not yet
```

## Scorecard

| Area | Score | Assessment |
| --- | ---: | --- |
| Purpose fit | 8.5 / 10 | Fits the JUMI runtime-helper role well. |
| Feature completeness | 7.0 / 10 | Has run, inspect, input, output, manifest, and termination flows. |
| PID 1 / graceful shutdown | 6.5 / 10 | Direction is right, but process-group residue handling is incomplete. |
| Artifact correctness | 7.0 / 10 | Good success/failure manifest policy, but background-writer edge cases remain. |
| Security | 7.2 / 10 | Stronger than a basic helper; path, symlink, and remote-fetch defenses exist. |
| Code quality | 6.0 / 10 | `helper.go` is too large and mixes too many responsibilities. |
| Tests | 7.0 / 10 | Broad unit coverage, but real signal and PID 1-style tests are missing. |
| Operations | 6.0 / 10 | Failure reasons and observability need more production hardening. |
| Optimization | 6.5 / 10 | Streaming hash is good, but large-output and directory-artifact behavior is weak. |

## Why The Score Is Near 7

`nan` is not just a command wrapper that runs a process and scans files. It
already includes several runtime-grade behaviors:

- user command execution
- exit code preservation
- run timeout handling
- SIGTERM/SIGINT/SIGHUP/SIGQUIT handling during command execution
- process-group signal forwarding
- shutdown grace period and SIGKILL escalation
- child subreaper setup
- best-effort zombie reap
- output path escape prevention
- symlink escape prevention
- input materialization
- remote-fetch digest verification
- node-local CAS promotion
- atomic manifest write
- termination summary recording

The remote fetch and path-security work are meaningful. Query-bearing signed
URLs are rejected, host allowlists are supported, private and loopback network
targets are guarded, and DNS rebinding is considered. This is more careful
than a typical ad hoc runtime helper.

## Why It Is Not Yet 8+

The score is capped because the core responsibility of `nan` is runtime and
artifact correctness. The current weak spots are in exactly those areas.

### 1. PID 1 Supervision Is Incomplete

The current implementation starts the user command in a separate process group,
but direct child completion is still treated as the main completion signal.

That is not enough for shell-heavy workloads:

```sh
sh -c 'sleep 60 & exit 0'
```

In that case the shell can exit successfully while descendants remain alive.
If `nan` proceeds to inspect outputs at that point, it can publish a manifest
for files that are still being written.

Production-grade supervision needs:

- stored PGID immediately after `cmd.Start()`
- signaling by stored PGID, not by `Getpgid(cmd.Process.Pid)`
- process group existence checks
- direct-child-exit cleanup checks
- SIGTERM wait for process group disappearance
- SIGKILL fallback with a hard deadline

See `docs/NAN_PID1_SUPERVISOR_REVIEW.md` for the detailed supervisor findings.

### 2. Artifact Correctness Has Shutdown Edge Cases

The high-level manifest policy is right:

```text
command success   -> inspect outputs -> manifest
command failure   -> no success manifest
timeout           -> no success manifest
external signal   -> no success manifest
```

The remaining correctness risk is:

```text
direct child exits 0
background writer continues
nan hashes output
success manifest is published for partial data
```

For JUMI/AH this is serious because downstream nodes may trust and consume the
published artifact.

To improve this area:

- fail if process group residue remains after direct child exit
- suppress manifest emission after any incomplete shutdown
- check cancellation immediately before manifest rename
- consider an explicit artifact finalization barrier for future versions

### 3. Code Structure Is Too Monolithic

`pkg/runtimehelper/helper.go` has too many responsibilities in one file:

- configuration
- run orchestration
- inspection
- input materialization
- HTTP fetch and remote-fetch security
- output validation
- CAS promotion
- signal handling
- subreaper and reap logic
- manifest writing
- path security
- environment parsing
- contract conversion

This increases maintenance risk and makes PID 1 behavior harder to test in
isolation.

Recommended structure:

```text
pkg/runtimehelper/
  run.go
  config.go
  supervisor.go
  signals_linux.go
  materialize.go
  remote_fetch.go
  outputs.go
  cas.go
  manifest.go
  paths.go
  contract.go
```

The first and most valuable extraction is `supervisor.go`.

## Area Notes

### Purpose Fit

Purpose fit is strong. `nan` is correctly positioned as:

```text
a runtime shim that turns a tool execution into a JUMI-compatible execution record
```

It should not become `bwa`, `samtools`, or a scheduler. It should wrap the
workload, enforce runtime boundaries, and report artifact state.

### Feature Completeness

The current flow is a good MVP skeleton:

```text
Validate
-> MaterializeInputs
-> executeCommand
-> classify failure / timeout / interruption
-> EmitArtifacts on success
-> write termination summary
```

The main feature ambiguity is directory output. `AllowDirectoryOutput` exists,
but deterministic directory artifact hashing is not implemented. For v0, it is
better to explicitly reject directory outputs until a tree-hash contract exists.

### PID 1 / Graceful Shutdown

The direction is right:

- separate process group
- OS signal subscription
- process-group signal forwarding
- grace period
- SIGKILL escalation
- subreaper setup
- WNOHANG reap attempt

The missing distinction is:

```text
direct child exit != process group exit
```

That distinction must be enforced before calling the supervisor production
grade.

### Security

Security is above average for this stage. Good existing guardrails include:

- local output path checks
- output root escape prevention
- symlink escape prevention
- symlink recheck before open
- manifest directory symlink check
- remote-fetch host allowlist
- private, loopback, and link-local network target rejection
- DNS rebinding defense
- signed URL query rejection
- input digest verification
- node-local source root validation

Remaining commercial hardening areas:

- decide whether `ManifestPath` must be under `OutputRoot`
- clarify redirect policy for remote fetch
- consider stronger `openat` / `O_NOFOLLOW` style file access in later hardening
- define command environment allowlist and secret redaction policy

### Tests

The test suite is meaningful, but the most important production behavior still
needs stronger coverage:

- subprocess test that sends SIGTERM to `nan`
- background descendant test: `sh -c 'sleep 60 & exit 0'`
- SIGTERM-resistant child or grandchild test
- process-group SIGKILL confirmation
- no manifest during shutdown
- direct child exits while process group remains alive
- container-like PID 1 e2e test
- large-output hashing cancellation test

### Optimization

Streaming hashing is good because output files are not loaded fully into
memory. That said, bioinformatics outputs can be very large. Future operational
work should consider:

- CAS copy cost
- same-device rename opportunities
- one-pass hash and promote behavior
- progress and metrics
- large-file fsync cost
- timeout or SIGTERM during sync/write/rename

## Path To 8+

The fastest path to an 8+ readiness score is:

1. Strengthen process supervision.
2. Split supervisor code out of the helper monolith.
3. Add real signal and process-group tests.
4. Clarify directory output policy.
5. Make manifest write context-aware.

### Priority 1: Supervisor Hardening

Required:

- store PGID after `cmd.Start()`
- add `processGroupExists(pgid)`
- check for process-group residue after direct child exit
- forbid success manifest if residue remains
- wait for process group disappearance after SIGTERM
- send SIGKILL after grace
- add a hard deadline after SIGKILL

### Priority 2: Structure Split

Extract:

- `supervisor.go`
- `materialize.go`
- `outputs.go`
- `manifest.go`
- `paths.go`
- `cas.go`

This is not cosmetic. It makes PID 1 behavior testable without coupling it to
artifact inspection and HTTP fetch logic.

### Priority 3: Shutdown E2E Tests

Minimum test shape:

```text
nan run --shutdown-grace-period 100ms -- sh -c 'trap "" TERM; sleep 60'
kill -TERM <nan-pid>
expect:
  exit code 143
  process group gone
  no manifest
```

### Priority 4: Directory Output Policy

Choose one:

```text
A. directory output is explicitly unsupported
B. implement deterministic tree hashing
```

For v0, option A is safer.

### Priority 5: Context-Aware Manifest Write

Add context checks through atomic manifest write, especially immediately before
rename.

## Final Assessment

The current implementation deserves:

```text
6.8 / 10
```

The project direction is good. This is not a fundamentally wrong design; it is
a promising runtime shim that now needs production hardening.

The next release should prioritize shutdown correctness and artifact
finalization correctness over new features.
