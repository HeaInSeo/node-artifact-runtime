# Migration From JUMI

## Goal

Move the runtime-side helper and its JUMI/AH-facing runtime contract out of the JUMI
repository.

## Why

- JUMI is the execution coordinator, not the owner of runtime tool images.
- The helper belongs to the DAG node runtime contract.
- The base image should be reusable by multiple projects and may be packaged by
  NodeVault or NodeKit instead of this repository.

## Planned Steps

1. Keep legacy compatibility in JUMI while the split stabilizes.
2. Move helper source into this repository.
3. Freeze the helper/runtime contract here.
4. Convert representative tool images to inherit from the base image.
5. Remove the JUMI-image-as-runtime-image smoke shortcut.
6. Retire the legacy `jumi-output-helper` path when migration is explicit.
