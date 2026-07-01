# NAN PID 1 and Graceful Shutdown Plan

문서 상태: Draft v0.1
작성일: 2026-07-01
대상 프로젝트: `node-artifact-runtime` / `nan`

## 결론

`nan`은 DAG node 컨테이너 안에서 user command를 실행하는 runtime commandlet이다.

따라서 `nan`은 기본 실행 모델에서 PID 1로 동작할 수 있어야 한다.

```text
/usr/local/bin/nan run --contract /jumi/node-contract.json -- <user command argv...>
```

`tini` 같은 외부 init은 지원할 수 있지만 필수 dependency로 두지 않는다.

```text
/usr/bin/tini -- /usr/local/bin/nan run --contract /jumi/node-contract.json -- <user command argv...>
```

운영 권장 문구는 다음과 같다.

```text
nan is designed to run as PID 1 in node containers.
Using an outer init such as tini is supported but not required.
```

## 배경

`nan`은 sidecar나 daemon이 아니라 컨테이너 main entrypoint에 가까운 runtime shim이다.

대표 실행 구조는 다음과 같다.

```text
PID 1: nan
  child process group:
    bwa
    samtools
    shell pipeline
    Python/R subprocesses
```

이 구조에서는 `nan`이 단순히 `exec.CommandContext`로 child 하나만 실행하는 wrapper이면 부족하다.

Kubernetes는 컨테이너 종료 시 PID 1에 `SIGTERM`을 보낸다. `nan`이 이 신호를 user command process group 전체에 전달하지 못하면 partial output, orphan process, zombie process, 잘못된 manifest 생성이 발생할 수 있다.

## 책임 경계

`nan`이 담당한다.

```text
- user command process group 생성
- Linux child subreaper 등록
- SIGTERM/SIGINT/SIGHUP/SIGQUIT forwarding
- timeout 시 user command process group 종료
- graceful shutdown grace period 적용
- grace period 초과 시 SIGKILL escalation
- user command 종료 후 reparented child reap
- 종료/timeout/interruption 경로에서 artifact inspect 생략
- termination log summary 작성
- user command exit code 또는 signal 기반 exit code 보존
```

`nan`이 담당하지 않는다.

```text
- Kubernetes Pod/Job 생성
- retry 판단
- AH RegisterArtifact 직접 호출
- 외부 init 강제 설치
- tool-specific cleanup hook 구현
```

`tini` 또는 외부 init이 있다면 PID 1 orphan reaping을 추가로 도와줄 수 있다. 그러나 `nan -> user command` 구간의 signal forwarding과 artifact correctness는 여전히 `nan` 책임이다.

## 종료 정책

### 정상 성공

```text
nan starts user command
user command exits 0
nan inspects declared outputs
nan writes manifest atomically
nan writes termination manifest
nan exits 0
```

### user command 실패

```text
user command exits non-zero
nan does not inspect outputs
nan writes command_failed summary
nan exits with the same exit code
```

### Kubernetes termination signal

```text
nan receives SIGTERM/SIGINT/SIGHUP/SIGQUIT
nan forwards the signal to the user command process group
nan waits for shutdown grace period
if process group exits:
  nan writes interrupted summary
  nan exits with signal-style exit code
if process group does not exit:
  nan sends SIGKILL to the process group
  nan writes killed summary
  nan exits with signal-style exit code
```

Artifact inspect must not run after an external termination signal.

### run timeout

```text
JUMI_RUN_TIMEOUT expires
nan sends SIGTERM to the user command process group
nan waits for shutdown grace period
if process group does not exit:
  nan sends SIGKILL to the process group
nan writes timeout summary
nan exits ExitTimeout
```

Timeout must cover the entire `Run` lifecycle:

```text
materialize inputs
execute user command
inspect outputs
write manifest
```

If timeout fires during inspect or manifest writing, `nan` should stop before publishing a success manifest.

## Configuration

Add one grace-period setting.

```text
flag: --shutdown-grace-period
env:  JUMI_SHUTDOWN_GRACE_PERIOD
default: 25s
```

The default should be lower than the Kubernetes `terminationGracePeriodSeconds` used by JUMI.

Recommended relationship:

```text
JUMI_SHUTDOWN_GRACE_PERIOD < terminationGracePeriodSeconds
```

Example:

```text
JUMI_SHUTDOWN_GRACE_PERIOD=25s
terminationGracePeriodSeconds=30
```

## Exit Code Policy

Recommended policy:

```text
normal success: 0
user command non-zero: preserve user command exit code
run timeout: ExitTimeout
SIGTERM: 143
SIGINT: 130
SIGKILL after grace period: 137 or ExitTimeout when caused by run timeout
helper config/materialization/inspect errors: existing nan helper exit codes
```

For timeout-triggered shutdown, prefer `ExitTimeout` even if the final child status is `SIGKILL`.

For Kubernetes/external signal-triggered shutdown, prefer signal-style exit codes so operators can read Pod status naturally.

## Implementation Plan

### Phase 1: Process lifecycle primitive

Goal: make command execution PID 1-safe enough for shell pipelines and subprocesses.

Tasks:

```text
- replace direct CommandContext kill behavior with explicit lifecycle control
- start user command in a process group
- register nan as a Linux child subreaper where supported
- forward incoming signals to the process group
- add shutdown grace period config
- send SIGTERM then SIGKILL on timeout/grace expiry
- reap reparented children after user command lifecycle completes
- return structured execution result: exit code, signal, timed out, interrupted
```

Acceptance tests:

```text
- sh -c "sleep 60" exits on RunTimeout
- sh -c "sleep 60 & wait" leaves no child process after timeout
- reparented orphan children are reaped before artifact inspect
- SIGTERM to nan terminates shell child and grandchild
- user command exit code is preserved
```

### Phase 2: Artifact correctness under shutdown

Goal: never publish a success manifest for interrupted or partial outputs.

Tasks:

```text
- skip EmitArtifacts after signal interruption
- skip EmitArtifacts after timeout
- make Run timeout apply to inspect/manifest phase
- write explicit termination summary for timeout/interrupted/killed
```

Acceptance tests:

```text
- interrupted run does not create artifact manifest
- timeout during command does not create artifact manifest
- timeout during slow inspect does not create success manifest
- termination log contains status and exit code
```

### Phase 3: Contract and docs hardening

Goal: make runtime contract behavior explicit for JUMI, NodeVault, and NodeKit.

Tasks:

```text
- document PID 1 support in architecture docs
- document optional tini mode
- add shutdown grace period to runtime contract docs
- validate unknown contract schemaVersion
- clarify contract-vs-flag precedence
```

Acceptance tests:

```text
- unknown node contract schemaVersion is rejected
- shutdown grace period can be set by env and flag
- contract loading behavior is documented and covered
```

### Phase 4: Runtime image guidance

Goal: give image builders a stable operational recommendation.

Tasks:

```text
- document default ENTRYPOINT using nan directly
- document optional tini-wrapped ENTRYPOINT
- define recommended Kubernetes terminationGracePeriodSeconds
- add smoke fixture for a representative tool-style shell pipeline
```

Acceptance tests:

```text
- local smoke command exercises nan -> shell -> long-running child
- failure and signal behavior are visible in termination log
```

## Proposed Schedule

Assuming one engineer and current repository size:

```text
Day 1:
  Phase 1 implementation
  process group lifecycle tests

Day 2:
  Phase 2 artifact correctness changes
  timeout/signal termination log tests

Day 3:
  Phase 3 contract/docs hardening
  schemaVersion and config tests

Day 4:
  Phase 4 runtime image guidance
  representative shell pipeline smoke test
  review with JUMI integration assumptions

Day 5:
  buffer for edge cases
  CI/lint/security pass
  migration notes update
```

If JUMI-side Pod command generation also changes in the same window, reserve one additional day for integration testing.

## Risk Register

### Process group behavior differs by platform

The production target is Linux containers. Tests that depend on Unix process groups should be Linux-only or guarded.

### Signal exit code policy can surprise retry logic

JUMI must treat timeout, external termination, and user failure separately. Termination summary should carry a machine-readable status.

### Slow output hashing can outlive timeout

Run timeout must be checked before and during artifact emit. Large output hashing should observe context cancellation.

### Optional tini can mask nan bugs

CI should test direct `nan` execution without `tini`. `tini` support is an operational hardening option, not the primary correctness mechanism.

## Reporting Checklist

Use this checklist for progress reports.

```text
- Process group lifecycle implemented:
- Signal forwarding implemented:
- Grace period implemented:
- Timeout uses graceful shutdown:
- Interrupted runs skip manifest:
- Timeout covers inspect/manifest:
- Contract schemaVersion validation added:
- Docs updated:
- Tests passing:
- Known residual risks:
```

## Current Recommendation

Proceed with direct `nan` PID 1 support first.

Keep `tini` documented as an optional outer init for environments that already standardize on it, but do not make it part of the required runtime contract.
