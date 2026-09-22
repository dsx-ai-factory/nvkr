# ADR-10 — Pluggable builder types (OCI-hermetic · Nix · VM image · prebuilt)

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-10 in the consolidated NKD ADR set.
- **Relates to:** ADR-07 (reproduction identity & builder profile), ADR-08 (bake-off gate).

- **Scope:** The numbered builder contract is for qualified execution; the later Decision sections define v1
  source lanes and disk compilation. Nix, VM execution and fused large-output wrapped jobs remain capability-gated;
  the admitted BuildKit wrapped lane has the narrower contract below.

## Context

NKD builds a Kubernetes substrate from pinned upstream inputs and preserves the evidence appropriate to each
producer. Qualified components may be reproduced exactly; wrapped aggregate artifacts remain pipeline-attested
until their own qualified subjects reproduce. NKD will use **Nix** both to provide repeatable build environments
and to build certain components (notably Kubernetes upstream), so the architecture must **orchestrate more than
one builder**.

The SRD is **neutral on build substrate**. It imposes two relevant constraints: the core must be releasable under
**Apache-2.0** with no *incompatible dependency linked into* it, and only a **qualified** substrate falls inside
the supported/reproducibility boundary (alternatives may be qualified in a later release). No SRD requirement
rejects a particular build system. A single hermetic-OCI build lane was an early *design choice*; this ADR
supersedes it with a builder abstraction and defines Nix as a proposed builder type. Both SRD constraints
hold, and the trust/verify model is untouched.

## Decision

Adopt a **builder abstraction**: one intermediate representation (IR), many builder types — the build-execution
analogue of "one IR, many CI adapters." Nix is **orchestrated as a subprocess** inside a pinned container, never
linked into the core, exactly as other external tools are treated. That satisfies the Apache-2.0 linkage
constraint, and Nix can enter the reproducibility boundary only after its execution and observation adapters pass acceptance.

1. **Taxonomy (two axes).** An acquisition **lane** — `from-source` XOR `prebuilt` XOR `exclusion` — remains the
   top-level discriminator. A **builder type** refines *from-source only*:
   `builderType ∈ {oci-hermetic, nix, buildkit-rootless, vm-image}`.
   The `prebuilt` lane is restricted to byte-preserving fetch plus digest verification. Repackaging,
   normalization, conversion, patching, or any other byte transformation is a build node with a builder type,
   pinned transformation inputs, and profile-bound output (ADR-07); it cannot be hidden inside `prebuilt`
   acquisition. Exclusions remain catalog declarations but cannot be selected as artifact producers; an ordinary
   output requires a nonempty producer set.

2. **What "nix" means.** A `nix` component's artifact is produced from a **locked flake** (`flakeRef` + a fully
   pinned lock over all transitive inputs + an output attribute). "Nix as an *environment* provider" is **not** a
   separate builder; it is a flake-pinned toolchain source feeding an otherwise `oci-hermetic` build. The
   builder-type set stays small and closed.

3. **Granularity: per-component.** Each component declares its builder type. A build may mix `nix`-built and
   `oci-hermetic`-built components, which is safe because verification is builder-agnostic (§5).

4. **Identity.** The builder type and its pins — the container-image digest and flake lock for `nix`, the
   toolchain digest for `oci-hermetic` — are **build-semantic inputs**, part of the IR and therefore of
   `buildDigest`. Switching a selected component's builder changes its generated graph's **Build Digest**;
   no authored build object or variant classifier is involved. For a lowered `vm-image`,
   its selected producer form, exact source-owned entrypoint, declared variable and material bindings, pinned
   toolchain closure, and output contract are likewise build-semantic inputs.

5. **Verification is builder-agnostic and subject-scoped.** Verify recomputes and compares the exact **artifact
   digests** covered by the requested claim and is indifferent to how each artifact was produced. A dependency's
   reproduced digest cannot satisfy its consumer's subject. The trust, attestation, and verification model is
   therefore unchanged; a builder type is only a new way to *populate* a build step.

6. **Uniform invocation.** All builders are invoked through the **same lowering path**, so the local runner and
   every CI backend emit an identical, byte-comparable invocation. `nix` runs inside a **pinned, digest-addressed
   container image**, and its output is extracted from the Nix store result. Artifact packaging and output
   selection remain an orthogonal axis and are not overloaded to carry builder type.

7. **Hermeticity.** A `nix` build's locked input closure is **pre-realized in a network fetch step**, after which
   the build runs **offline**: no network, sandbox on, no external substituters. This mirrors the existing
   fetch-then-offline-build discipline and preserves the no-network-during-compile invariant and the air-gap
   story.

8. **Build isolation (mechanism deferred to a spike).** Nix's build sandbox is in tension with NKD's deliberately
   least-privileged build isolation. The **target is rootless / user-namespace Nix** in the pinned container:
   sandbox without full privilege escalation. The exact privilege set is a **de-risking spike**, and the resulting
   privilege profile becomes an **output-affecting builder-profile field** (ADR-07). Fallback: single-user Nix
   with the kernel sandbox off, backstopped by the two-machine bake-off.

9. **Reproducibility: verify, don't trust.** Nix raises the probability of bit-identical output — the reason it
   fits hard-to-reproduce upstreams like Kubernetes — but does not guarantee it for every derivation. NKD's
   guarantee stays **verification-based**: the same verify re-hash **and** the two-machine bake-off apply to `nix`
   components, and the builder profile pins the Nix version and sandbox configuration as output-affecting
   (fail-closed preflight when a `nix` component is present).

10. **Catalog adoption.** Acceptance requires demonstrating `nix` on at least one real component; that capability is not yet implemented. Which components a given catalog builds with Nix is a **catalog-content** decision, not part of
    this ADR.

### Source lanes for v1 production

Each component has exactly one declared production source lane, separate from the qualified builder taxonomy:

| Lane | Contract |
|---|---|
| `hermetic-build` | Pinned source commit/tree and toolchain container; enforced build network denial |
| `wrapped-build` | Pinned external entrypoint at a pinned commit, inside a pinned container with declared network and privilege |
| `acquired` | Digest-pinned upstream bytes; no build claim |

The lane must agree in provenance, Release membership, evidence records, and milestone scope. Unknown or
indeterminate lanes are definition errors. The `buildkit-rootless` builder implements the wrapped-build lane
for OCI-export archives on Docker, locally and in generated lifecycle Runs. Other wrapped adapters and Nix
qualified execution remain deferred. A component may select its own digest-pinned toolchain image while
retaining the same pinned source as other components; this changes its build identity, not its source pin.

### Disk-image authoring contract

`disk-image` is an output semantic, independent of how its bytes are obtained. Every such component declares
`disk: {format: qcow2, encryption: none|luks2}`. `vm-image` is one generic from-source builder class for that
output; it is not implied by `disk-image`, so another admitted builder or a future digest-pinned acquisition lane
can produce the same output type. The first schema admits only the from-source form; acquired disks remain
unsupported. Conversely, `vm-image` is valid only for a from-source disk component.

A disk Definition output selects the disk root and every component in its transitive material closure. Catalog
resolution identifies the one root from `outputType: disk-image`; no second root field or implicit catalog
selection exists. A missing or second root, a missing transitive material, and any unrelated selected component
fail admission.

A `vm-image` component selects exactly one producer form. The existing `buildCommand` field represents the
command form; the presence of a `packer` object represents the direct-Packer form. A producer form is an
invocation mechanism, not a builder type or an output semantic:

- `command` records one exact source-owned argument vector. The executable may be `make`, a repository script,
  or another pinned entrypoint. The current argument contract has only the existing `{{.Arch}}` substitution; it
  has no material-manifest placeholder, fixed path, positional argument, or environment-variable handoff. NKD
  does not infer one or interpret the Makefile or script. Material-bearing command producers therefore remain
  authoring-only until this ADR and the catalog add an explicit typed handoff before lowering is admitted.
- `packer` invokes a pinned Packer executable against a source-owned HCL project and one exact Packer build name.
  NKD does not generate, rewrite, or interpret HCL. Packer plugins and the VM toolchain are authenticated parts
  of the pre-realized toolchain closure, not downloads discovered during image construction. The Packer
  executable and every host executable that Packer or a plugin resolves through `PATH` are exact-digest inputs;
  admitted execution constructs `PATH` only from that held closure and never exposes the ambient host path.
  The held-executable boundary admits only Linux ELF files. Scripts and other binfmt-dispatched formats fail
  capability admission because holding their bytes does not authenticate the selected host interpreter. The
  outer realized-runtime adapter must additionally authenticate the ELF dynamic loader and shared-library
  closure; a future script lane must bind its interpreter in that same closure.

The direct-Packer authoring form names the stable boundary that compilation records and future execution must enforce:

```yaml
builderType: vm-image
packer:
  projectPath: os-image/packer
  packerVersion: 1.14.2
  buildName: nke-node-non-fde.qemu.nke-non-fde
  architectureVariable: architecture
  acceleratorVariable: accelerator
  materialManifestVariable: material_manifest
  materialManifest: # optional producer-owned wire projection
    schemaVersion: nke.dev/os-image-materials/v1
    kinds:
      image-inputs: archive
      installer-iso: disk-image
  outputDirectoryVariable: output_directory
  sshPrivateKeyVariable: ssh_private_key_file # optional as a pair
  sshPublicKeyVariable: ssh_public_key_file   # optional as a pair
  buildLogDirectoryVariable: build_log_directory # optional source contract
  httpDirectoryVariable: http_directory           # optional source contract
  variables:
    image_variant: standard
materials:
  image-inputs: nke-image-inputs
  installer-iso: ubuntu-installer-iso
artifactPath: nke-node.qcow2
```

`projectPath` is relative to the pinned source root. `buildName` is the exact compiled build name execution
passes as one `-only=<buildName>` argument; Packer documents names such as
`my_build.amazon-ebs.first-example`. NKD rejects Packer's list and glob selection syntax because one component
produces one declared artifact. Catalog loading checks bounded UTF-8 argument transport that rejects control,
format, line/paragraph-separator and non-ASCII space-separator characters, distinct bindings, and exact
`buildName` selection without list or glob syntax. It does not parse HCL or claim
that the named build or variables exist. Before any image construction or output collection, admitted lowering
must invoke the pinned Packer executable's `validate` command against the pinned project, the exact build name,
and the declared variable bindings, and fail closed on any diagnostic, including a warning returned with exit
status zero when `-only` matches no build. For a named HCL build block, the selector is the complete compiled
name (for example `nke-node-non-fde.qemu.nke-non-fde`), not only the source type and source name. `artifactPath` is relative to the private
output directory bound through `outputDirectoryVariable`. `architectureVariable` always carries the selected
target architecture. `acceleratorVariable` always carries the admitted `kvm` or `tcg` route; a source-owned
default cannot override that decision. The required variable names and any source-exposed log or HTTP directory
mapping are explicit and distinct, so NKD never guesses HCL names. Cache, temporary, configuration and home
directories remain runtime-owned standard paths rather than authored HCL-variable mappings.
The optional SSH private/public variable-name pair, when declared, receives paths to an attempt-local keypair
created inside the private execution root. That handoff remains reserved and unrealized until the authenticated
OCI executor supplies the attempt-local keypair. The variable names affect Build identity; generated key bytes and
paths affect only the Run and never enter the catalog, logical Build identity, logs or evidence.
`packerVersion` is an exact logical input. The admitted runtime compares it with the Packer executable inside the
digest-pinned OCI toolchain before validation. The source-owned HCL constrains required plugins; the same OCI
digest authenticates their preinstalled bytes, and execution exposes only its fixed read-only plugin directory.
`packer.variables` contains only public literal strings. It is not a
credential channel, and future invocation records expose these values. The pinned Packer subprocess remains
authoritative for checking their names and values against source-owned HCL declarations.

Packer v1.14.2's
[`validate` implementation](https://github.com/hashicorp/packer/blob/v1.14.2/command/validate.go) emits the clean
success line only after diagnostic handling returns zero, and its
[`validate` tests](https://github.com/hashicorp/packer/blob/v1.14.2/command/validate_test.go) fix clean stdout to
exactly `The configuration is valid.` plus a newline while demonstrating that warnings can precede that line on
stdout with exit status zero. The connected local OCI driver therefore accepts only that clean line (ignoring
surrounding whitespace) with empty stderr; any other output fails closed. Driver admission and lifecycle are
contract-tested, but a recorded real pinned Docker/Packer/NKE execution remains required before maintained NKE
support, product acceptance, or any pipeline-attested or qualified production claim; until then the local result
stays unqualified. A local 2026-09-18 framing probe used the official Packer 1.14.2 Darwin ARM64 ZIP
(`sha256:b053c01272f87fe669c642dfac167b0b48cfb5c119e2e946d90ef92c2407f746`, matching HashiCorp's published
`SHA256SUMS`) and Packer-installed Docker plugin 1.1.2. The full compiled selector returned exit zero, the single
28-byte success line on stdout, and empty stderr; a short selector returned exit zero, a 251-byte warning plus
the same success line on stdout, and empty stderr. This probes validation framing only, not NKE execution or disk
production.

`materials` is a one-to-one map from a source-owned binding name to another explicitly selected component in
the same output's build graph. The target may be a from-source build or a digest-pinned acquisition. Unknown,
excluded, self-referential, duplicate-target, cyclic, or omitted targets fail catalog validation or build-graph
resolution. The declaration does not create a second component graph or implicitly select producers; it adds
typed edges between the existing selected component outputs. Source-owned HCL defaults do not authorize NKD to
infer or fetch undeclared build materials.

Compilation lowers each declared binding to the exact selected build or acquisition producer and records both
the typed binding and graph dependency. Before either driver may execute, required component outputs must be built or acquired, verified, and assembled
into a deterministic build-material manifest. By default, each v2 entry binds its source-owned binding name,
observed component name, output kind, selected target architecture, exact artifact digest, and a slash-relative
path derived from the binding name beneath a private staging root. A Packer producer may instead declare one
`materialManifest` projection with its source-owned `schemaVersion` and one producer-visible `kind` for every
declared binding. The projection must cover the binding set exactly and is a logical Build input. It changes only
the serialized schema and kind vocabulary; authenticated bytes, component, architecture, digest and deterministic
staging path remain the same. NKD has no source-specific conditional or implicit kind translation. Binding,
component, schema and kind text use the same display-safe UTF-8 policy as producer transport.
Random or absolute execution-root paths never enter its canonical bytes. The manifest is a
deterministic description, not an authenticated receipt; admitted execution must derive it from authenticated
producer observations and bind the exact serialized file bytes before invoking a subprocess. Direct Packer will
receive the private absolute manifest path through its declared `materialManifestVariable`; the source must
cross-check every entry's architecture against the separately supplied architecture variable. The command/Make form
has no equivalent executable handoff in the current schema and therefore cannot lower a material-bearing command.
Adding one requires a typed authored field and identity/lowering contract; an adapter may not invent a conventional
path, append an argument, or inject an undeclared environment variable.

Malformed manifest schema or fields, including an absent explicit materials array or malformed digest text, fail
as `invalid-catalog`. They are description-shape failures, not missing provenance or a failed authenticated
reference verification.

Admitted direct-Packer lowering must bind only declared inputs and variables, expose the authenticated source
read-only, provide private output, log and HTTP locations, invoke `validate` and then the exact selected build,
collect the declared artifact, and recompute its exact bytes. It does not run `packer init`, fetch plugins, or
acquire source or product materials. Build execution remains network-none; all required bytes must already be in
the authenticated source and material closure. A driver
may not replace a staged material with an undeclared network fetch. The catalog entry owns the pinned source and
toolchain, selected driver, exact entrypoint or Packer project and build name, artifact path, material edges, and
public Packer-variable values. These fields are logical IR v4 inputs and therefore affect Build identity.
Compilation also projects the root disk artifact directly into the named lifecycle output; it does not package
the QCOW2 as a binary bundle. This compiler result grants no execution, observation, evidence, or publication
authority.

The executable name grants no evidence lane. A Make-driven image may qualify when its exact command, staged
materials, toolchain closure, enforced isolation, output collection, disk inspection, and reproduction evidence
satisfy the same contracts as a direct Packer build. A direct Packer invocation receives no weaker controls and
no qualification merely because NKD recognizes its driver. Networked or opaque acquisition inside either driver
remains pipeline-attested or fails qualified admission, according to the selected lane. Make also remains a valid
command entrypoint for non-VM builders under their existing contracts.

Material producers retain their own evidence scope. A package rebuilt byte-identically may carry qualified
reproduction evidence, and a byte-preserving acquisition may carry exact digest verification without an upstream
build claim. The VM-image root does not inherit either claim. Rebuilding that root from the same authenticated
closure and reconciling read-only disk inspection establishes controlled production and composition; it does not
establish disk reproduction. The root remains pipeline-attested unless ADR-07/08/14 independently admit and
compare its exact qualified disk subject.

Secret-valued Packer inputs are not public variables and may not be transported in process arguments, environment
variables, manifests, logs or evidence. The plain-image direct contract needs no such input. FDE remains closed
until its source-owned contract provides an authenticated file-path secret handoff or generates and removes an
attempt-local bootstrap secret without exposing it through NKD's public variable transport.

Runner capacity is not part of `vm-image`. A from-source component may declare generic `buildRequirements`
using ADR-18's per-step execution contract. A `vm-image` producer must use that same generic type to declare a
complete profile: positive CPU, memory and ephemeral storage, at least one eligible runner-host platform, an
explicit KVM policy, and a positive executor timeout. That request is reusable by every builder type, changes
catalog and Plan identity, and never proves that the selected runner satisfies it. The request can constrain minimum CPU,
memory and ephemeral storage, an OR-set of eligible runner-host platforms, typed host capabilities, and the
executor wall-time budget. Runner-host platform is distinct from output target platform and the IR guest/output
platform. For `vm-image`, per-architecture toolchain pins name execution-host platforms rather than the guest
architecture. Every allowed host route needs an exact per-host toolchain pin; the scalar source fallback does
not select VM execution bytes. Compilation binds the selected host platform into logical Build identity even
when two host routes name the same OCI manifest digest. `kvm` is the first
typed capability: `required` must be exposed and requires an allowed host platform matching the resolved guest
architecture, `forbidden` must be withheld, and `preferred` retains the cross-architecture TCG route. A future
Plan compilation binds KVM or the fallback before work rather than choosing opportunistically at runtime. Build network
remains the existing IR semantic and is not duplicated here.

NKE host CPU, memory, storage, platform, KVM policy, and timeout values remain unauthored until supported routes
are measured. Guest vCPU, guest memory, and logical disk size are not host requests. Produced-artifact size is
an artifact and transport concern rather than a runner-capacity field.

For the [NKE Makefile at commit `6df693a`](https://github.com/NVIDIA/nke/blob/6df693a9748a78aafdf96e1f2d03772530783487/os-image/packer/Makefile#L148-L185),
the following source-owned targets illustrate the `command` driver. Plain and FDE are separate selectable
components, not a public boolean or variant. The existing typed architecture token is resolved from the output target platform:

```text
make -C os-image/packer os-image-non-fde ARCH={{.Arch}}
  -> os-image/packer/output-non-fde/nke-node-non-fde-ubuntu-26.04.1-{{.Arch}}.qcow2
make -C os-image/packer os-image-fde ARCH={{.Arch}}
  -> os-image/packer/output-fde/nke-node-fde-ubuntu-26.04.1-{{.Arch}}.qcow2
```

The current NKE release matrix realizes plain amd64, FDE amd64, and plain arm64. It does not validate or publish
FDE arm64. Definition output selection records that supported matrix without duplicating components per
architecture. A maintained NKE component may instead select the `packer` driver after NKE exposes an authenticated
staged-material input and private-output contract in its source-owned HCL. That catalog choice changes catalog
snapshot and Build identity. It does not remove or invalidate the standalone Make entrypoint. Compiler lowering
supports a direct-Packer producer with typed materials and a command producer without materials. A
material-bearing command/Make producer, VM execution, disk collection, and publication remain capability-gated.
An override that changes NKE's derived
output filename must also change the catalog artifact path; NKD never guesses it.

The BuildKit mode declares network access and fixed rootless container settings: UID/GID 1000, unconfined
seccomp/AppArmor/system paths for user-namespace operation, the overlayfs snapshotter, no host runtime socket
and no mounted credentials. Rootless overlay mounts require a supporting kernel (Linux 5.11+ or Ubuntu,
per the pinned BuildKit rootless contract). Unsupported hosts fail; there is no native-snapshotter fallback
that silently multiplies source/toolchain storage for each Dockerfile step. No `/dev/fuse` device is required.
Its writable output mount lives beneath a private host parent, is collected after
container exit, and is then re-hashed and imported as an exact OCI export. The package inspector verifies the
image manifest, platform, config and compressed/uncompressed layer identities. Neither those checks nor a
signed production predicate claim hermeticity, independent reproduction, or qualified execution. Qualified
and direct in-container execution reject this mode before source work.

A wrapped build makes no NKD hermeticity claim and requires neither builder-profile preflight nor bit-identity
comparison in v1. Fuse build, re-hash, Run record, publish, sign, and verify into one job for large artifacts,
preserving that exact order. Fetch privately held inputs first using a read-only credential; no credential may
reach the build phase. Registry/OIDC credentials appear only after build exit and recorded observation.
Prefer a pinned privileged container to an unpinned host build. The renderer must reject undeclared privilege,
network, or credentials during build before output. An external reusable workflow is not a pinned entrypoint.

`embeds` declares components inside the external artifact that NKD neither builds nor separately delivers.
Names, versions and known source pins feed production dependencies, the aggregate SBOM dependency graph, and a
qualification cross-check against the upstream version manifest **at the pinned commit**. Missing or disagreeing
members fail that check. This declaration does not prove the image's observed contents. ADR-13 separately
requires traced materials and read-only final-disk inspection before disk delivery is admitted; those inventory
adapters remain deferred. Embedded-software correctness and runtime validation remain the upstream build
owner's duty. Where ADR-22's curated set exists, derive `embeds` from it and re-derive at validation. Both
`embeds` and the curated-set machinery remain deferred.

## Consequences

**Positive**
- `from-source` retains two strong-guarantee sub-types and adds a capability-gated VM-image authoring type;
  `prebuilt` remains the weak lane.
- Hard-to-reproduce upstreams become buildable via Nix without changing the trust model.
- Builder-type selection does not weaken the existing verification, attestation, or CI-adapter contracts;
  disk evidence, canonical delivery, and publication contracts are owned by ADR-13, ADR-14, and ADR-20.
- NKD becomes builder-agnostic — additional builder types plug in at the same seam.

**Negative / costs**
- Nix build isolation under least-privilege needs a spike and may require a documented, profile-tracked privilege
  concession.
- A build using `nix` requires an operator builder profile that *provides* Nix at the pinned version — a higher
  bar for those operators.
- The builder profile and reproduction tuple gain builder-type-aware fields (ADR-07).

**Neutral**
- `buildDigest` changes for any component moved to `nix` — by design; a builder switch is a new build.

## Alternatives considered

- **Keep a single OCI-hermetic substrate.** Rejected — insufficient once Nix is required for repeatable
  environments and Kubernetes builds.
- **Link Nix into the core.** Rejected — violates the Apache-2.0 linkage constraint.
- **Nix-only.** Rejected — loses the environment-agnostic goal and the prebuilt/no-compile lane.
- **Model "Nix as environment" as a separate builder type.** Rejected — mode-explosion across the runner and CI
  backends; folded into `oci-hermetic` as a toolchain source instead.

## De-risking spikes

1. **Nix-in-container reproducibility & isolation** — prove a Nix build reproduces bit-identically **offline**,
   **sandboxed**, under **least-privilege** in a pinned container; record the minimal privilege set.
2. **Locked-closure pre-fetch & air-gap** — prove the locked flake closure can be pre-realized or mirrored and the
   build runs offline against it, byte-stably.

## Implementation note

The change is localized to a single build-execution seam: a per-component builder attribute on the build step, a
builder dispatch in the runner, and the one shared CI-lowering path. Because every backend lowers through that one
path, the local-vs-emitted byte-identity contract is preserved.

## Follow-ups

- **ADR-07:** `engineClass` gains `nix`; the builder profile gains a "provided builders" declaration plus the Nix
  version and privilege fields (fail-closed).
- **ADR-08:** the two-machine bake-off covers `nix` components.
- **SADD / design notes:** update *Design Alternatives* to reflect pluggable builders rather than a single chosen
  substrate.

## Acceptance evidence

Current module evidence in `pkg/nkd/buildgraph/nix_test.go`, `pkg/nkd/buildgraph/prebuilt_test.go`, and
`pkg/nkd/pipeline/pull_test.go` is partial. Acceptance requires an offline least-privilege execution of one real
Nix component and a test proving that byte-preserving acquisition has no profile while a deterministic
repackaging step changes the build graph and binds its output to the measured builder profile.
