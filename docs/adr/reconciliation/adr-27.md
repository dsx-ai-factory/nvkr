## Support by owner

| Owner | Implemented and contract-tested | Remaining capability or acceptance |
|---|---|---|
| 27 Pinned-tool producers (proposed) | ADR-27 units T1–T7. The catalog admits `builderType: pinned-tool` with `tool`, `toolArgs` and `toolEnv`, the `{{.Output}}`, `{{.Arch}}` and `{{.Material "<binding>"}}` token grammar, and the §11 refusals. The compiler lowers a selected producer to a source-less `KindBuild` whose tool digests, member, format and name are logical identity and whose release URL is transport. A `file` producer must be bound as a material by another selected producer; it is also a member of its output, which an executable bundle installs with mode 0644, and its per-artifact SBOM records the `wrapped-build` lane and executable digest. The factory shares one `tool/<name>` acquisition per name, adds an explicit tool input and refuses conflicting pins. The Linux executor re-hashes the staged executable before launch. It runs the tool with no network, a read-only root, read-only tool and material mounts, private `HOME`, `TMPDIR` and working directory, and a fixed environment and umask. It admits exactly one regular file and leaves no output on failure. Plans with a pinned-tool build use the `wrapped-build` production type and record the release URL and `nvkr:tool/<name>` dependencies; finalization rejects a missing or altered tool dependency. GitHub generation keeps both nodes on default hosted routing and stays byte-stable. T6 admits one layout root with its complete explicit regular-file material closure. Packaging preserves its manifest/config/layer bytes; publication retains the original index bytes and every producer subject. The executor already hands such a producer the absent path `/nvkr/output/layout`, because layout writers refuse an existing directory. It admits exactly one layout whose root holds `blobs`, `index.json` and `oci-layout` with no link or special file, moves it to the output root and observes its `index.json` digest | Execution bootstraps through the toolchain image's `/bin/sh` and `/usr/bin/env`. The shared acquisition refuses `zip` tools, and SBOM inspection refuses a `file` payload, both with `engine-capability`. Wall time is bounded by the caller's job context, because exec-node refuses per-node requirements. Qualified execution, direct pipeline packaging, directory forms, companion staging, the native ARC disk route, a runtime not named `docker` or `podman`, non-Linux hosts and a host whose platform differs from the target fail with `engine-capability`. Linux executor contract tests use a fake container runtime, and no Podman run exists. Unrecorded local Docker runs executed the AICR v1.2.0-rc1 recipe and Flux bundle producers; current converted-path execution evidence is tracked in [AICR deployment bundles](aicr-deployment-bundles.md); hosted Run and consumer verification remain unexercised |

## AICR-specific post-render validation

GB300 generic training Flux selects one Linux ARM64 AICR v1.2.0 release executable, one recipe file producer
and one layout producer. Other eight AICR components retain their source-built behavior. Factory imports an
exact private layout snapshot, the bounded archive owner extracts its admitted layer, and pipeline invokes
fixed `aicr verify` arguments with the same pinned executable and execution image. The child has no network,
read-only inputs, private temporary directories and a two-minute bound; its lifecycle uses the existing owned
subprocess and container reaper. A validation failure prevents the producer result and removes its output.
No authored command list, directory material or configurable validator is added.

The result records tool, original-index, manifest and layer digests as descriptions. Packaging rechecks the
bytes and recomputes each material manifest from the exact selected producer results. The release binding
retains original index bytes, so a consumer can recompute the link from the layout build subject to the
unchanged manifest. Materials retain file/composition evidence; the root and final manifest have separate
bounded file inventories. This integrity boundary does not alter ADR-27 §6's generic opaque-signature
handling or grant upstream-signature, qualified-execution, cluster or hardware acceptance. AICR's
unverified trust level remains distinct from an authenticated NVKR Release.

Contract coverage includes closure admission, index substitution, changed validation subjects, material
bindings and complete publication/evidence membership. Linux subprocess tests use a fake runtime; these
are separate from the recorded Docker execution and establish no hosted isolation acceptance.
