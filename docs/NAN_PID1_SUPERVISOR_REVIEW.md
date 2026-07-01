# nan PID 1 Supervisor Review

Status: post-v0.3.0 static review

Reviewed target:

- repository: `node-artifact-runtime`
- executable: `nan`
- release baseline: `v0.3.0`

## Summary

`nan` is moving in the right direction as a runtime shim for DAG node
containers. The `v0.3.0` work added the important first layer:

- user command execution in a separate process group
- SIGTERM/SIGINT/SIGHUP/SIGQUIT forwarding while the command is running
- timeout-triggered SIGTERM then SIGKILL escalation
- child subreaper setup
- timeout/interruption paths that avoid success artifact manifest emission

This is enough to call `nan` a PID 1-aware runtime shim. It is not yet enough
to call it a complete PID 1-safe supervisor.

The main remaining risk is artifact correctness when the direct child exits
but background descendants remain alive. In that case `nan` can inspect output
files too early and publish a success manifest for partial or still-mutating
artifacts.

## Correct Mental Model

The central problem is not that `nan` itself becomes a zombie. The problem is
that commands started by `nan` can leave children, grandchildren, shell
pipelines, or background writers behind.

Typical container process tree:

```text
PID 1: nan
  └─ user command process group
       ├─ bwa
       ├─ samtools
       └─ shell / python / subprocess
```

When `nan` runs as PID 1, it must own three runtime guarantees:

1. Forward Kubernetes termination signals to the whole user process group.
2. Reap reparented children and prevent zombie accumulation.
3. Never publish a success artifact manifest after timeout, interruption, or
   incomplete command shutdown.

## What Is Good In v0.3.0

### Process Group Execution

`nan` uses `SysProcAttr{Setpgid: true}` instead of relying on
`exec.CommandContext`. That is the right shape for bioinformatics tools,
shell pipelines, and subprocess-heavy workloads.

### Signal Forwarding Direction

The command phase listens for termination signals and forwards them to the user
command process group. This is required for Kubernetes pod termination because
the container runtime sends SIGTERM to PID 1.

### Failure Manifest Policy

The current policy is correct:

```text
command success   -> inspect outputs -> write manifest
command failure   -> no success manifest
timeout           -> no success manifest
external signal   -> no success manifest
```

### Output Path Guardrails

The implementation already validates output paths, rejects non-local path
escapes, checks symlink resolution under the output root, and writes manifests
atomically. These are good foundations for a runtime artifact inspector.

## P0 Issues

### P0. Direct Child Exit Can Hide Live Grandchildren

Current command waiting is centered on the direct child:

```go
waitCh := make(chan error, 1)
go func() {
    waitCh <- cmd.Wait()
}()
```

If the direct child exits while a background descendant is still alive,
`cmd.Wait()` completes and `nan` can proceed to artifact inspection.

Example:

```sh
sh -c 'sleep 60 & exit 0'
```

Risk:

```text
1. shell exits 0
2. background process remains alive
3. nan treats command as successful
4. nan hashes output while a descendant may still be writing
5. nan publishes an incorrect success manifest
```

Required behavior:

```text
direct child exits
  -> check whether the process group still exists
  -> if no remaining process exists, inspect outputs
  -> if remaining processes exist, terminate the group and fail the run
```

This must be fail-closed for DAG node workloads. A node command that leaves
background writers behind has not reached a trustworthy artifact boundary.

### P0. PGID Must Be Stored Immediately After Start

The current signal path resolves the process group from the direct child PID
when a signal is sent. That is fragile after the process group leader exits.

With `Setpgid: true`, the child PID is the process group ID. Store it
immediately after `cmd.Start()`:

```go
if err := cmd.Start(); err != nil {
    return commandResult{Err: err, ExitCode: ExitGenericError}
}
pgid := cmd.Process.Pid
```

All later signal delivery should use the stored PGID:

```go
syscall.Kill(-pgid, syscall.SIGTERM)
syscall.Kill(-pgid, syscall.SIGKILL)
```

Do not depend on `Getpgid(cmd.Process.Pid)` after the leader may have exited.

### P0. Signal Handling Covers Only Command Execution

The current signal handler is scoped to `executeCommand()`. The full `Run`
lifecycle is broader:

```text
MaterializeInputs
executeCommand
EmitArtifacts
atomic manifest write
termination log write
```

External SIGTERM during materialization, hashing, or manifest write should
cancel the whole lifecycle and prevent success manifest publication.

Required direction:

```text
Run starts
  -> create lifecycle shutdown context from parent context, timeout, and OS signals
  -> pass lifecycle context through materialization, command, artifact emit, and manifest write
```

## P1 Issues

### P1. Termination Waits Only For Direct Child

The current termination helper waits for `waitCh`, which represents only the
direct child. It should wait for both:

- direct child exit
- process group disappearance

Termination flow should be:

```text
send SIGTERM to -pgid
wait until:
  - direct child exited
  - process group disappeared
  - grace expired

if grace expired and the group still exists:
  send SIGKILL to -pgid
  wait for group disappearance or hard deadline
```

Process group existence can be checked with signal `0`:

```go
func processGroupExists(pgid int) bool {
    err := syscall.Kill(-pgid, 0)
    return err == nil || errors.Is(err, syscall.EPERM)
}
```

### P1. Subreaper Setup Should Not Always Be Fatal

`PR_SET_CHILD_SUBREAPER` is useful, but failure should not automatically block
the workload in all environments.

Recommended policy:

```text
if subreaper setup succeeds:
    continue
else if os.Getpid() == 1:
    warn and continue
else:
    warn by default; optionally fail in strict supervisor mode
```

This keeps `nan` operational under runtime/seccomp differences while still
surfacing the degraded supervision mode.

### P1. Manifest Atomic Write Should Be Context-Aware

`EmitArtifactsContext()` checks context before hashing and before calling the
manifest writer. The final atomic write itself does not accept a context.

At minimum, context should be checked:

- before creating the temp file
- after write/sync/close
- immediately before rename

The rename-preceding check is the important correctness boundary.

## P2 Issues

### P2. Reserved Commandlet Names Need Documentation

`nan` supports commandlet dispatch:

```text
nan run ...
nan inspect ...
nan version
nan <user command> ...
```

This means `run`, `inspect`, and `version` are reserved top-level commandlets.
If a real user command collides with one of these names, users must invoke it
through:

```text
nan run -- <command> ...
```

### P2. Directory Output Is Not Actually Supported Yet

`AllowDirectoryOutput` currently permits a directory past the early type check,
but downstream digest/CAS logic is file-oriented. Until a directory tree hash
contract exists, directory output should be explicitly unsupported in v0.

Recommended policy:

```text
allowDirectoryOutput=false by default
allowDirectoryOutput=true still returns unsupported until tree hashing exists
```

### P2. Manifest Path Boundary Should Be Explicit

Output artifacts are constrained under `OutputRoot`, but `ManifestPath` is not
currently required to be under `OutputRoot`.

Recommended policy options:

1. Require `ManifestPath` to be under `OutputRoot`.
2. Keep operator-controlled flexibility, but document the recovery contract and
   warn when the manifest path is outside the output root.

JUMI currently expects a predictable manifest path, so this should be made
explicit before broad runtime image rollout.

## Recommended Implementation Order

### 1. Extract Supervisor Logic

Move process supervision out of the large helper orchestration file.

Suggested structure:

```text
pkg/runtimehelper/
  helper.go       // Run / Inspect orchestration
  supervisor.go   // process group, signal, timeout, subreaper
  materialize.go  // input materialization
  inspect.go      // output inspect / digest
  manifest.go     // manifest write / termination log
  paths.go        // secure path helpers
```

The first extraction target should be `supervisor.go`.

Suggested API:

```go
type SupervisorConfig struct {
    Command     []string
    GracePeriod time.Duration
    Stdout      io.Writer
    Stderr      io.Writer
}

type SupervisorResult struct {
    ExitCode                 int
    Err                      error
    TimedOut                 bool
    Interrupted              bool
    Killed                   bool
    Signal                   syscall.Signal
    ProcessGroupNotClean     bool
}
```

### 2. Store PGID And Signal By Stored PGID

After `cmd.Start()`, persist the process group ID and use it for all later
signal delivery.

### 3. Fail On Remaining Process Group After Direct Child Exit

Add a cleanup check after direct child exit. If the process group still exists,
terminate it, return a command failure, and suppress artifact inspection.

### 4. Add PID 1-Oriented Tests

Required tests:

```text
TestRunFailsWhenBackgroundGrandchildOutlivesMainCommand
TestRunExternalSIGTERMStopsProcessGroupAndSuppressesManifest
TestRunKillsSIGTERMResistantGrandchild
```

The SIGTERM test should run `nan` as a child test process, send SIGTERM to that
process, and assert that no manifest is written.

### 5. Expand Signal Context To Whole Run Lifecycle

Create the lifecycle cancellation context at the start of `Run()` and use it
through:

- input materialization
- command supervision
- output inspection
- manifest write
- termination summary write where practical

## Final Assessment

`v0.3.0` is a good first PID 1-aware release. It should be described as:

```text
PID 1-aware nan runtime shim
```

It should not yet be described as:

```text
complete PID 1-safe init/supervisor
```

The next patch should focus on supervisor correctness before adding new runtime
features. The three non-negotiable fixes are:

1. Store PGID immediately after `cmd.Start()` and signal by stored PGID.
2. Detect and fail on process group residue after direct child exit.
3. Move OS signal cancellation to the full `Run()` lifecycle.
