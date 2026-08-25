# ADR-001: Separate image-build and MicroVM runtime egress

## Status

Accepted

## Date

2026-08-25

## Context

Lambda MicroVM image creation must pull the container base before a VM can
start. Live tests showed that assigning Whim's zero-route VPC connector to
image creation makes even a minimal public-base build enter `CREATE_FAILED`.
The same image builds successfully with the managed public connector.

The security requirement applies to the running workload: direct public IP
and public-hostname connections must fail, and runtime metadata must report the
validated isolated connector. DNS blocking is a separate requirement tracked
in issue #7.

## Decision

For the CLI, `--egress none` is a runtime policy:

- Resolve and validate the isolated VPC connector before building.
- Create the image with public egress and no isolated build connector.
- Persist the requested runtime policy, connector ARN, and validated topology.
- Launch with an explicit `EgressNone` connector override and revalidate the
  recorded topology immediately before `RunMicrovm`.
- Require runtime metadata to report exactly that connector; terminate the VM
  if metadata is missing or differs.

The library remains explicit: callers choose build egress in
`BuildFromSourceOptions` and runtime egress through launch options.

## Alternatives considered

### Bake the isolated connector into the image

Rejected because image creation cannot pull a public container base through a
zero-route connector and fails before runtime isolation can be tested.

### Require callers to prebuild an offline base

Rejected because it adds a separate image-distribution workflow when the
service already supports public image creation and runtime connector overrides.

## Consequences

- Network-dependent Dockerfile steps work under CLI `--egress none` because
  they execute during the public build phase.
- The built image may report `INTERNET_EGRESS`; only the launched VM's connector
  metadata is part of the no-public-egress acceptance criteria.
- A cached image can be reused across runtime policies, while every isolated
  launch still fails closed on connector or topology drift.
- This does not block AWS-provided DNS resolution; strict DNS remains separate.
