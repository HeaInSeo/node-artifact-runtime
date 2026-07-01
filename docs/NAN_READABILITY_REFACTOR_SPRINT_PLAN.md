# nan Readability Refactor Sprint Plan

Status: proposed

Basis:

- `docs/NAN_COMMERCIAL_READINESS_REVIEW.md`
- `docs/NAN_PRODUCTION_HARDENING_SPRINT_PLAN.md`
- post-`v0.3.1` readability assessment

Baseline:

- current release: `v0.3.1`
- current readability / maintainability estimate: `7.2 / 10`
- target after this plan: `8.0+ / 10`

## Goal

Improve developer readability and long-term maintainability without changing
the external runtime contract.

The previous hardening sprint made `nan` safer as a PID 1-aware supervisor.
This sprint makes the code easier to audit, extend, and review by separating
runtime responsibilities into focused files and by centralizing status/error
classification.

## Non-Goals

This sprint does not attempt to:

- add new artifact transport features
- implement directory tree hashing
- change JUMI/AH integration semantics
- change the CLI contract
- retag a release unless the refactor lands cleanly and validation passes

## Current Assessment

Current readability score:

```text
7.2 / 10
```

What improved after `v0.3.1`:

- process supervision moved into `supervisor.go`
- PID 1 behavior is easier to inspect
- `Run()` still has a readable high-level flow
- failure statuses are more explicit
- behavior tests now describe important shutdown cases

Remaining readability issues:

- `helper.go` still mixes too many responsibilities
- materialization, output inspection, CAS promotion, manifest writing, contract
  conversion, and parsing helpers share one large file
- status/error classification is still distributed across call sites
- Linux-specific supervisor behavior is not split from cross-platform
  orchestration
- public contract-facing types and internal runtime state are not clearly
  separated by file boundaries

## Target Structure

Recommended package structure:

```text
pkg/runtimehelper/
  run.go                 // Run / Inspect orchestration and lifecycle summaries
  config.go              // Config, input/output specs, validation
  supervisor.go          // cross-platform command supervision orchestration
  supervisor_linux.go    // prctl, wait4, process group behavior
  materialize.go         // MaterializeInputs and local/remote dispatch
  remote_fetch.go        // HTTP fetch, DNS, allowlist, redirect policy
  outputs.go             // output inspection and artifact records
  cas.go                 // node-local CAS promotion
  manifest.go            // atomic manifest and termination log writes
  paths.go               // secure path helpers
  contract.go            // contract conversion
  env.go                 // env parsing and command env construction
```

This is a direction, not a requirement to create every file immediately. The
sprint should split along tested responsibility boundaries and avoid mechanical
churn that does not improve reviewability.

## Sprint Structure

Recommended duration: 2 weeks

```text
Week 1: file boundary split and status classification cleanup
Week 2: Linux split, docs, validation, release decision
```

## Week 1: Responsibility Boundaries

Objective:

Reduce `helper.go` from a catch-all file into a readable orchestration layer.

### Work Items

1. Move config and validation into `config.go`.

   Acceptance:

   - `Config`, `InputSpec`, `OutputSpec`, and validation live outside
     `helper.go`.
   - no behavior change.
   - existing config/contract tests pass.

2. Move materialization into `materialize.go`.

   Acceptance:

   - `MaterializeInputs`, local reuse, and materialized input path logic are in
     a dedicated file.
   - remote HTTP fetch implementation can remain together initially if moving
     it separately would create noisy churn.

3. Move output inspection into `outputs.go`.

   Acceptance:

   - `EmitArtifactsContext`, `buildArtifactRecordContext`, output digest, and
     output type checks are separated from run orchestration.
   - directory output unsupported policy remains explicit.

4. Move manifest and termination writes into `manifest.go`.

   Acceptance:

   - `atomicWriteFileContext`, `writeTerminationSummary`, and
     `writeTerminationManifest` live together.
   - context-aware manifest write tests still pass.

5. Centralize status/error classification.

   Acceptance:

   - command result, lifecycle cancellation, inspect failure, materialization
     failure, and process-group residue statuses have a single classification
     point where practical.
   - string statuses remain stable unless an intentional contract change is
     documented.

### Required Tests

```text
go test ./pkg/runtimehelper
go test ./cmd/node-artifact-runtime
```

### Week 1 Exit Criteria

- `helper.go` mainly reads as orchestration.
- behavior test output is unchanged.
- no external CLI or contract behavior changes.

## Week 2: Platform Split And Final Polish

Objective:

Make platform-specific supervisor behavior easier to reason about and finish
readability documentation.

### Work Items

1. Split Linux-specific supervisor code.

   Acceptance:

   - `supervisor.go` owns high-level supervision flow.
   - Linux primitives such as `PR_SET_CHILD_SUBREAPER`, `Wait4`, and process
     group signaling live in `supervisor_linux.go`.
   - non-Linux behavior is either explicitly no-op where already supported or
     isolated behind small helper functions.

2. Move path helpers into `paths.go`.

   Acceptance:

   - secure output, input, node-local, and relative path helpers are grouped.
   - path security tests continue to pass.

3. Move contract/env conversion into focused files.

   Acceptance:

   - contract-to-config conversion is in `contract.go`.
   - env parsing and command env construction are in `env.go`.

4. Add a package map.

   Acceptance:

   - README or a doc section explains the runtimehelper file responsibilities.
   - future contributors can find supervisor, materialization, manifest, and
     output logic without reading the entire package.

5. Final release decision.

   Acceptance:

   - if the refactor is behavior-preserving, tag as patch release only after
     validation.
   - if any observable status/contract behavior changes, document it and
     reassess release number.

### Required Tests

```text
go test ./...
go test -race ./pkg/runtimehelper
```

### Week 2 Exit Criteria

- file boundaries match the target structure closely enough for review.
- `helper.go` is no longer the primary location for every runtime concern.
- platform-specific supervisor behavior is isolated.
- tests pass with race coverage for `runtimehelper`.

## Suggested Implementation Order

1. Create `config.go` and move type definitions plus validation.
2. Create `manifest.go` and move manifest/termination writes.
3. Create `outputs.go` and move artifact inspection.
4. Create `materialize.go` and move materialization dispatch.
5. Create `paths.go` and move path helpers.
6. Split `supervisor_linux.go` from `supervisor.go`.
7. Create `env.go` and `contract.go` if the remaining helpers are still noisy.
8. Run full tests and compare behavior.

## Risk Management

### Risk: Mechanical Refactor Hides Behavior Changes

Mitigation:

- move code in small commits or tightly scoped patches
- run tests after each boundary split
- avoid renaming statuses, fields, and env vars during file moves

### Risk: Package Fragmentation Without Clarity

Mitigation:

- split only around real responsibilities
- keep related small helpers near their callers if moving them makes code harder
  to follow
- maintain a package map in docs

### Risk: Linux Split Introduces Build Issues

Mitigation:

- keep signatures small
- add build tags only if needed
- run `go test ./...` after the split

## Definition Of Done

This sprint is done when:

1. `helper.go` is reduced to orchestration and small shared helpers.
2. supervisor, manifest, materialization, output inspection, CAS, paths, env,
   and contract conversion have clear file ownership.
3. status/error classification is easier to audit.
4. Linux process supervision details are isolated.
5. `go test ./...` passes.
6. `go test -race ./pkg/runtimehelper` passes.
7. README or docs describe the file map.

## Expected Outcome

Expected readability / maintainability score:

```text
8.0 - 8.3 / 10
```

This sprint should make future production hardening cheaper. It should also
make reviews more precise because changes to supervisor, manifest, CAS,
materialization, and contract behavior will appear in focused files instead of
one large helper implementation.
