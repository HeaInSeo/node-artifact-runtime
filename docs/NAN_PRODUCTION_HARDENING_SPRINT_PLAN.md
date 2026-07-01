# nan Production Hardening Sprint Plan

Status: proposed

Basis:

- `docs/NAN_PID1_SUPERVISOR_REVIEW.md`
- `docs/NAN_COMMERCIAL_READINESS_REVIEW.md`

Baseline:

- current release: `v0.3.0`
- current readiness estimate: `6.8 / 10`
- target readiness after this plan: `8.0+ / 10`

## Goal

Move `nan` from a PID 1-aware MVP runtime shim to a production-candidate
runtime supervisor for JUMI DAG node containers.

The main objective is not adding features. The objective is to make command
shutdown and artifact finalization trustworthy under Kubernetes termination,
timeouts, shell pipelines, and background descendants.

## Non-Goals

This sprint does not attempt to:

- implement directory tree artifact hashing
- redesign JUMI scheduling or AH registry semantics
- introduce a new runtime base image pipeline
- add broad observability systems beyond failure reason improvements needed by
  this hardening work

## Release Target

Recommended release target:

```text
v0.3.1
```

Rationale:

- The work is hardening and correctness-focused.
- The existing CLI and contract shape can remain compatible.
- If the implementation introduces a contract-visible termination reason or
  behavior that downstream systems must explicitly interpret, reassess whether
  `v0.4.0` is more honest before tagging.

## Sprint Structure

Recommended duration: 3 weeks

```text
Week 1: P0 supervisor correctness
Week 2: lifecycle signal correctness and tests
Week 3: structure split, policy cleanup, release validation
```

## Week 1: Supervisor Correctness

Objective:

Make process-group supervision fail-closed and independent of the direct child
remaining alive.

### Work Items

1. Store PGID immediately after `cmd.Start()`.

   Acceptance:

   - `executeCommand` or the new supervisor stores `pgid := cmd.Process.Pid`
     after a successful start.
   - all process-group signal delivery uses the stored PGID.
   - signal delivery no longer depends on `Getpgid(cmd.Process.Pid)` after
     process start.

2. Add process group existence checks.

   Acceptance:

   - `processGroupExists(pgid int) bool` is implemented for Linux.
   - the implementation treats `nil` and `EPERM` from `kill(-pgid, 0)` as
     "exists".
   - invalid PGID values are rejected.

3. Detect process-group residue after direct child exit.

   Acceptance:

   - direct child exit does not automatically mean successful command
     completion.
   - if the process group still exists after direct child exit, `nan` sends
     SIGTERM then SIGKILL if needed.
   - artifact inspection is skipped when process-group residue is detected.
   - termination summary clearly reports a process supervision failure such as
     `process_group_not_clean`.

4. Change termination wait semantics.

   Acceptance:

   - termination waits for both direct child completion and process group
     disappearance.
   - grace timeout escalates to SIGKILL.
   - SIGKILL has a hard follow-up deadline to avoid hanging forever.

### Required Tests

- `TestRunFailsWhenBackgroundGrandchildOutlivesMainCommand`
  - command: `sh -c 'sleep 60 & exit 0'`
  - expected: no success manifest, remaining group terminated, non-zero exit
    or explicit supervision failure

- `TestSupervisorUsesStoredPGIDAfterLeaderExit`
  - verifies signaling does not depend on resolving PGID from a dead leader

- existing timeout and SIGTERM-resistant child tests still pass

### Week 1 Exit Criteria

```text
go test ./pkg/runtimehelper ./cmd/node-artifact-runtime
```

passes locally, and the background descendant case cannot publish a success
manifest.

## Week 2: Full Lifecycle Shutdown Semantics

Objective:

Handle external signals across the whole `Run()` lifecycle, not only during
user command execution.

### Work Items

1. Add lifecycle signal context at `Run()` start.

   Acceptance:

   - `Run()` creates a lifecycle context that observes parent cancellation,
     run timeout, and OS termination signals.
   - the lifecycle context is passed to materialization, command supervision,
     artifact emission, and manifest write.

2. Classify external shutdown outside command execution.

   Acceptance:

   - SIGTERM during materialization returns an interrupted/terminated result,
     not a generic materialization failure.
   - SIGTERM during artifact hashing suppresses success manifest emission.
   - termination summary uses a clear status.

3. Make manifest write context-aware.

   Acceptance:

   - add `atomicWriteFileContext`.
   - check context before temp creation, before write, after sync/close, and
     immediately before rename.
   - timeout or SIGTERM before rename prevents final manifest publication.

4. Add real external SIGTERM test.

   Acceptance:

   - test starts `nan` as a subprocess.
   - test sends SIGTERM to the `nan` process.
   - expected exit code is signal-derived, typically `143` for SIGTERM.
   - manifest is absent.
   - child/grandchild process group is gone.

### Required Tests

- `TestRunExternalSIGTERMSuppressesManifest`
- `TestRunExternalSIGTERMDuringArtifactHashSuppressesManifest`
- `TestAtomicManifestWriteHonorsCanceledContextBeforeRename`

### Week 2 Exit Criteria

```text
go test ./...
```

passes, including subprocess signal tests.

## Week 3: Structure, Policy Cleanup, And Release Readiness

Objective:

Reduce maintenance risk and close visible policy ambiguities before tagging the
next release.

### Work Items

1. Extract supervisor code.

   First target structure:

   ```text
   pkg/runtimehelper/
     helper.go
     supervisor.go
     supervisor_linux.go
   ```

   Acceptance:

   - process group, signal, subreaper, reap, and termination wait logic move
     out of `helper.go`.
   - existing behavior remains covered by tests.
   - public API surface stays minimal.

2. Clarify directory output policy.

   Recommended v0 policy:

   ```text
   directory output is explicitly unsupported
   ```

   Acceptance:

   - `AllowDirectoryOutput` no longer implies support that does not exist.
   - either reject directory outputs explicitly or mark the config field as
     reserved/experimental with fail-closed behavior.
   - docs mention deterministic tree hashing as future work.

3. Document reserved commandlets.

   Acceptance:

   - README or runtime shim design doc states that `run`, `inspect`, and
     `version` are reserved top-level commandlets.
   - command name collision guidance is documented:

     ```text
     nan run -- <command> ...
     ```

4. Decide manifest path boundary policy.

   Acceptance:

   - choose one policy:
     - require `ManifestPath` under `OutputRoot`
     - or allow operator-controlled path with explicit documentation
   - tests cover the chosen behavior.

5. Release validation.

   Acceptance:

   - `go test ./...`
   - `go test -race ./pkg/runtimehelper ./cmd/node-artifact-runtime` where
     feasible
   - JUMI `runtime-align-check` passes after pin update in a follow-up JUMI
     change

### Week 3 Exit Criteria

The implementation is tag-ready when:

- all P0 supervisor issues are closed
- all Week 1 and Week 2 tests pass
- helper file size and responsibility concentration are reduced
- docs reflect actual directory output and commandlet behavior
- release notes clearly state shutdown semantics

## Backlog After This Sprint

These should not block `v0.3.1` unless they become necessary during
implementation:

- deterministic directory tree hashing
- `openat` / `O_NOFOLLOW` style path hardening
- command environment allowlist and secret redaction
- large-output progress metrics
- CAS promotion optimization for same-device rename
- richer structured termination reasons for JUMI UI/observability
- container-level PID 1 e2e test in CI

## Risk Management

### Risk: Process Group Detection Is Race-Prone

Process group state can change between checks.

Mitigation:

- design checks as best-effort but fail-closed
- after direct child success, require a clean group before manifest emission
- tolerate `ESRCH` as group gone

### Risk: Tests Leave Processes Behind

Supervisor tests can leak sleeping processes if assertions fail.

Mitigation:

- every test must have cleanup with SIGKILL fallback
- use short sleep/grace values
- avoid relying on global process names for cleanup

### Risk: Signal Tests Kill The Test Runner

Sending SIGTERM to the current process can terminate `go test`.

Mitigation:

- run `nan` behavior in a subprocess helper
- send SIGTERM only to the child process
- isolate temp directories and manifests per test

### Risk: Compatibility With Existing JUMI Behavior

Failing on background process residue may reveal workflows that accidentally
daemonize work.

Mitigation:

- treat this as correct fail-closed behavior
- document the policy
- ensure termination summary explains the cause

## Definition Of Done

This sprint is done when:

1. `nan` stores PGID and supervises by stored PGID.
2. Direct child success cannot publish a manifest while process group members
   remain.
3. External SIGTERM is respected across the full `Run()` lifecycle.
4. Manifest write is context-aware before final rename.
5. PID 1-oriented subprocess tests are present and passing.
6. Supervisor logic is separated enough to test and maintain independently.
7. Directory output and reserved commandlet policies are documented.

## Expected Outcome

After this sprint, `nan` should be described as:

```text
production-candidate PID 1-safe runtime shim
```

The expected readiness score should move from:

```text
6.8 / 10
```

to approximately:

```text
8.0 - 8.3 / 10
```

assuming the supervisor hardening and signal tests land without regressions.
