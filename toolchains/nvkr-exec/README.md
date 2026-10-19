# Helper build scaffold

This directory builds the `nvkr-exec` helper and records per-architecture byte digests. It is development tooling,
not a qualified publisher. [ADR-18](../../docs/adr/0018-pipeline-ir-and-ci-adapter-emission.md) owns isolation and
helper admission; [ADR-21](../../docs/adr/0021-generated-pipeline-maintenance-and-lifecycle.md) owns regeneration
and upgrades.

| File | Purpose |
|---|---|
| `build-nvkr-exec.sh` | Build static Linux helpers for amd64/arm64 twice and reject unequal bytes; optionally assemble images |
| `Dockerfile` | Package the helper with a rootless-podman runtime base |
| `helper-image.lock.json` | Unsigned captured binary/image metadata; regenerate with the script |

From the repository root, with Bash 4 or later and the repository's Go toolchain:

```sh
make nvkr-exec-helper
make nvkr-exec-helper ARGS=--image
```

The script replaces its `out/` directory and lock. Image assembly additionally needs a container runtime and a
real digest-pinned `RUNTIME_BASE`. It never pushes or signs. The committed lock has a placeholder runtime base
and null image digests; its source reference and binary hashes describe that captured build, not the current
working tree. The image recipe's unpinned package installation still prevents a reproducible image claim.

Two matching builds on one host establish local repeatability only. Independent reproduction also needs exact
source/toolchain inputs and a separate execution. A helper binary digest and its enclosing image digest are
separate identities. Helper/runtime configuration belongs to Environment or the qualified builder profile;
component toolchains remain Recipe inputs.

Qualified helper-image execution remains unavailable until the exact executable/runtime observation protocol
is implemented. A published, signed image alone cannot satisfy that admission gate. Existing image-mode
rendering contracts do not establish qualified provider execution. Use the current source-bootstrap workflow
and the [implementation record](../../docs/adr/reconciliation.md) for the supported boundary.
