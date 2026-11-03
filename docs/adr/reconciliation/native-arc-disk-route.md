## Native ARC disk route
The single checked-in `nvkr-run.yml` selects the 69-output M2 Definition and its main-only publication
Environment, including plain and public-bootstrap FDE disks on both native architectures. The Makefile drift
gate repeats the exact Definition and Environment paths recorded in the committed Plan. Dispatch does not
choose among Definitions. Alternate selections can be generated into a scratch root with their matching
byte-exact check. The standalone `codex/fde-image-pipeline` branch used the separate one-output FDE Definition
and build-only Environment for the recorded diagnostic below. Neither candidate selection is a ratified
canonical-product/path migration or publication-namespace decision; that named-decider governance remains
pending. On October 13, 2026, the
`nkd-arc` Environment admitted the exact FDE branch through deployment policy `62934956`, preserving its
existing `main` and M2 validation-branch policies. That provider configuration is not execution evidence.

The build-only finalizer uses the selected native ARC pool and matching helper with a two-hour bound,
restoring and rehashing the complete raw-disk closure without publication authority. Comparable AMD64
material, source and disk-package carriers in [Run 37533806963](https://github.com/dsx-ai-factory/nvkr/actions/runs/37533806963)
totaled 15,362,463,697 compressed bytes. This prior measurement motivated the runner choice. The FDE
attempt below completed on ARC; peak scratch usage was not measured, so it establishes no general
capacity bound.

On October 13, 2026, [Run 38443178035](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035)
completed all ten generated jobs on `codex/fde-image-pipeline` at commit
`68c219845398b8ef5890e5c76a13d4901a22630d`, attempt `38443178035-1`.
The [native job](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/job/115457416653)
ran for 43m 07s, including setup, a 29m 48s grouped build step and all uploads.
Its log records successful Packer target `nke-node-fde.qemu.nke-fde`.
The [finalizer](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/job/115483887874)
completed in 6m 22s on a fresh native runner, restoring the complete closure and checking its exact bytes.
The retained Run has `status: complete`, `lane: static-unqualified`, one expected and observed
`nvke-node-fde-amd64` disk output, its eleven-component closure and no failure:

- Plan Digest: `sha256:3f5db9dda12b12accfe2ba10567b5d1b00e99538c469ee2d32ad631e5a91dba7`.
- Output Build Digest: `sha256:b5c4dbb201dbac6e8f5498d67a2bba297f2ee4824766b0b6cf59f3d21a7e19b8`.
- Raw QCOW2 payload: `sha256:c3d6b6694f1b0c626b1df0f61b10a60a5f2bd9fac9d0c37ff2407a0b7b85cede`,
  6,738,411,520 bytes (6.28 GiB).
- Run content subject: `sha256:5c2d10edb27c52e7ae99b8d0def2566762ae11a1e5a07db5feb708bb552dd75a`.

The [image artifact](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11840209683),
[producer result](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11841075777),
[terminal observation](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11840159679)
and [final Run record](https://github.com/dsx-ai-factory/nvkr/actions/runs/38443178035/artifacts/11841676055)
retain the evidence. The payload digest and size agree across the producer and finalized Run; they are
the raw QCOW2 identity, not the Actions ZIP identity. This proves the pinned FDE-target bake and generated
build-only workflow for this attempt. The recorded `luks2` declaration is not independent guest inspection,
boot or TPM-enrollment validation, deployed-clone confidentiality, reproducibility, qualification or
publication evidence. The pinned-source audit's key-material and firstboot limitations still apply.

ADR-18 now permits the explicit `githubARC: native-kvm-v1` operator-trusted policy for the two native KVM pools.
The grouped helper retains original material and disk owners through reconciliation and packaging; generation
contracts cover both architectures, individual-node bypass rejection and unsupported policy rejection. For
publication workflows, source fetch, bootstrap and release credentials remain outside ARC jobs. Native preparation/measurement is provided
separately. M2 now generates all 69 named outputs with four native disk jobs from the same Definition,
an exact catalog overlay and measured native inspection profiles. Four toolchain images were
published to internal GHCR packages under the user's scoped candidate exception, with exact
manifest/config read-back. This is not completed organizational license/security review.
Private pulls use a separate packages:read step whose temporary credentials are removed before
execution; cached exact image identities are revalidated by the held runtime.
Both native plain-image builds and terminal checks completed in run `37445475840`; both deep disk inspections timed out.
The user earlier moved deep disk/content inspection and dependent reconciliation out of M2. The narrower candidate
`diskEvidence: build-provenance-v1` selects live producer/material evidence with signed uninspected scope,
normal packaging/signing/publication and unchanged subject/provenance/membership/completeness gates.
The [inspection deferral record](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-inspection-deferral.md) records the scope and validation.
The plain-image strict inspection path remains available. M2 retains its AMD64-only product acceptance floor and accepts
producer provenance with its remaining delivery/consumer gates. The approved initial scope also
retains producer-only disk evidence and defers inspection/material reconciliation to hardening. This task
scope supersedes the earlier M3 assignment without changing any historical signed bytes or establishing
organizational ADR ratification. The native ARM64 route and this candidate Definition's
additional ARM64 outputs are an implementation extension. All four native disk jobs and every declared output
remain required for this candidate's complete Run and Release; the product floor waives none of those requirements.
Organizational ADR ratification remains separate.
Both candidate disk jobs succeeded in run `37486058412`, but the aggregate release download failed with ENOSPC
before publication. The [dated carrier evidence](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-m2-release-carrier-transport.md) records
that failed attempt. The subsequent validation-branch run `37533806963` at commit `625980e` passed
all candidate outputs and aggregate publication, signing, verification and finalization. That historical run
predates the consumer-policy and timeout changes here; it is not evidence for this updated head, current candidate
execution, strict inspection or independent full-payload consumer acceptance. Per-output storage peaks remain unmeasured.
The [October 6 preparation record](https://github.com/dsx-ai-factory/nvkr/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-native-arc.md) retains final-source
ARM64 and AMD64 Packer/inspector assembly, executable measurements, native tool validation
and unprivileged libguestfs appliance filesystem probes. The earlier preparation Environment admitted
`codex/m2-arc-validation` through `githubDispatchBranches` and its signer identity.
The October 7 PR #97 test temporarily authored `codex/m2-native-arc-foundation` as an exact dispatch/signing
exception. [Run 37661490433](https://github.com/dsx-ai-factory/nvkr/actions/runs/37661490433)
at `562a609` completed with conclusion `cancelled`. Provider annotations rejected the exact branch under
`nkd-release` (sample attestation job `112931037919`, finalizer `112933977061`) and `nkd-arc`
(native ARM64 job `112932239664`); the failed jobs had no executed steps. This is a deployment-policy refusal,
not disk-build, signing or release acceptance evidence. The temporary authored exception was removed after
that cancelled test; dispatch and signer policy select `main` only again.
The external policy read-back retained `codex/m2-arc-validation` and `main` for `nkd-arc`, and
`codex/m2-arc-validation`, `feat/nke-deploy-pipeline` and `main` for `nkd-release`; it did not admit the exact
PR #97 branch. Removing an authored exception neither changes those provider policies nor bypasses them.
The existing provider secret is `NKE_SOURCE_TOKEN`; the rename retains that exact
credential reference, not an unprovisioned `NVKE_SOURCE_TOKEN`. Dedicated reader-account package access, native pool capacity and successful exact-head
execution remain external acceptance requirements.
