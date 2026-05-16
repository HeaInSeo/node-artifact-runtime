# node-artifact-runtime

Runtime-side artifact contract assets for DAG node containers.

## Scope

This repository is intentionally separate from JUMI and AH.

It owns:

- the `node-artifact-runtime` helper binary
- JUMI/AH integration contract docs
- contract tests for runtime-side artifact export

It does not own:

- the JUMI service process
- the AH service process
- pipeline scheduling policy
- the long-term node runtime base image packaging pipeline

## Relationship To ko

Kubernetes data-plane service apps such as JUMI are expected to standardize on `ko`
for their service-image builds.

This repository is adjacent to that policy, but not identical to it.

- JUMI service image: should follow the `ko`-based data-plane app build direction
- `node-artifact-runtime`: runtime-side helper contract, not a JUMI service image
- base image packaging: may be handled by NodeVault or NodeKit using this contract

## Intended Model

tool image
  FROM <NodeVault or NodeKit published runtime base image>
  installs bwa or gatk or samtools or python toolchain
```

The helper runs inside the DAG node runtime container and wraps the user command:

```text
/usr/local/bin/node-artifact-runtime -- <user command>
```

## Current Status

This repository is the landing zone for the helper and JUMI/AH-facing runtime
contract split.

The helper implementation still exists in the JUMI repository for compatibility during
the migration. Code and build definitions will be moved here incrementally.

## Initial Layout

- `cmd/node-artifact-runtime`: future helper entrypoint
- `docs`: architecture and migration notes

## Near-Term Tasks

1. Move the helper source from JUMI into this repository.
2. Freeze the JUMI/AH-facing runtime contract documentation.
3. Hand off the documented contract to NodeVault/NodeKit for base-image work.
4. Add contract tests for manifest generation and exit-code preservation.
