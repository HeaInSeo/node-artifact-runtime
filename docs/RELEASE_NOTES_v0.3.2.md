# nan v0.3.2 Release Notes

Date: 2026-07-02

## Summary

`v0.3.2` is a release-consistency and validation patch on top of `v0.3.1`.
It keeps the `v0.3.1` PID 1 supervisor behavior and records the post-hardening
state in CI, docs, and the runtime version string.

## Changes

- Reports `nan version` and manifest `nanVersion` as `v0.3.2`.
- Keeps directory outputs explicitly fail-fast:
  - directory outputs return `ExitUnsupportedOutputType`.
  - success artifact manifests are not emitted for directory outputs.
  - deterministic directory tree artifact support remains tracked separately.
- Adds CI coverage for the PID 1 container smoke path.
- Keeps the local smoke target:

```text
make smoke-pid1-container
```

## Validation

Required validation for this patch:

```text
go test ./...
go test -race ./pkg/runtimehelper
scripts/nan-pid1-container-smoke.sh
```

## Tracking

- Directory output policy and future deterministic tree artifact support:
  https://github.com/HeaInSeo/node-artifact-runtime/issues/1
