# Architecture

## Purpose

`node-artifact-runtime` is a runtime-side executable for DAG node containers.

It exists to align files generated inside a node runtime container with the artifact
contract consumed by JUMI and AH.

## Boundary

This repository owns the runtime-side helper and its JUMI/AH-facing contract.

JUMI consumes:

- helper path contract
- manifest contract
- runtime env contract

AH consumes:

- registered artifact metadata
- lifecycle notifications from JUMI

Neither JUMI nor AH should treat this helper as part of their service image.

NodeVault or NodeKit may later own the actual base-image packaging that embeds this
helper.

JUMI itself, as a Kubernetes data-plane service app, should move toward `ko` for its
service-image build path. That `ko` direction does not imply that this helper belongs
inside the JUMI service image.

## v0 Runtime Behavior

The helper should:

1. wrap the user command
2. preserve the user command exit code
3. inspect declared outputs or `/out`
4. compute digest and size metadata
5. write an artifact manifest for JUMI to read back

## Base Image Direction

Representative tool images should inherit from a base image that already contains the
helper binary.

```text
published runtime base image
  - /usr/local/bin/node-artifact-runtime
```

This repository documents the contract that such a base image must satisfy.
It does not need to own the base-image packaging pipeline itself.

## Migration Note

JUMI currently still carries a legacy helper named `jumi-output-helper` for smoke/dev
compatibility. The intended long-term runtime-side name is `node-artifact-runtime`.
