# ADR-10 — Pluggable builder types (OCI-hermetic · Nix · prebuilt)

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-10 in the consolidated NKD ADR set.
- **Relates to:** ADR-07 (reproduction identity & builder profile), ADR-08 (bake-off gate).

- **Scope:** The numbered builder contract is for qualified execution; the final Decision section defines v1 source lanes. Nix and wrapped execution remain capability-gated.

## Context

NKD builds a Kubernetes substrate from upstream source, reproducibly. It will use **Nix** both to provide
repeatable build environments and to build certain components (notably Kubernetes upstream), so the architecture
must **orchestrate more than one builder**.

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
   top-level discriminator. A **builder type** refines *from-source only*: `builderType ∈ {oci-hermetic, nix}`.
   The `prebuilt` lane is restricted to byte-preserving fetch plus digest verification. Repackaging,
   normalization, conversion, patching, or any other byte transformation is a build node with a builder type,
   pinned transformation inputs, and profile-bound output (ADR-07); it cannot be hidden inside `prebuilt`
   acquisition. Exclusion is unchanged.

2. **What "nix" means.** A `nix` component's artifact is produced from a **locked flake** (`flakeRef` + a fully
   pinned lock over all transitive inputs + an output attribute). "Nix as an *environment* provider" is **not** a
   separate builder; it is a flake-pinned toolchain source feeding an otherwise `oci-hermetic` build. The
   builder-type set stays small and closed.

3. **Granularity: per-component.** Each component declares its builder type. A recipe may mix `nix`-built and
   `oci-hermetic`-built components, which is safe because verification is builder-agnostic (§5).

4. **Identity.** The builder type and its pins — the container-image digest and flake lock for `nix`, the
   toolchain digest for `oci-hermetic` — are **build-semantic inputs**, part of the IR and therefore of
   `recipeDigest`. Switching a component's builder produces a **new `recipeDigest`, i.e. a new recipe** — never a
   "variant" (variants cover same-recipe *representation* drift only).

5. **Verification is builder-agnostic.** Verify recomputes and compares **artifact digests** and is indifferent to
   how an artifact was produced. The trust, attestation, and verification model is therefore unchanged; a builder
   type is only a new way to *populate* a build step.

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
retaining the same pinned source as other components; this changes its Recipe identity, not its source pin.

The BuildKit mode declares network access and fixed rootless container settings: UID/GID 1000, unconfined
seccomp/AppArmor/system paths for user-namespace operation, the native snapshotter, no host runtime socket and
no mounted credentials. Its writable output mount lives beneath a private host parent, is collected after
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
members fail that check. NKD does not inspect the image to prove its contents; that remains the upstream build
owner's duty. Where ADR-22's curated set exists, derive `embeds` from it and re-derive at validation. Both
`embeds` and the curated-set machinery remain deferred.

## Consequences

**Positive**
- `from-source` gains two strong-guarantee sub-types; `prebuilt` remains the weak lane.
- Hard-to-reproduce upstreams become buildable via Nix without changing the trust model.
- Verification, attestation, and the CI-adapter contract are **unchanged**; the change is confined to the build
  step.
- NKD becomes builder-agnostic — additional builder types plug in at the same seam.

**Negative / costs**
- Nix build isolation under least-privilege needs a spike and may require a documented, profile-tracked privilege
  concession.
- A recipe using `nix` requires an operator builder profile that *provides* Nix at the pinned version — a higher
  bar for those operators.
- The builder profile and reproduction tuple gain builder-type-aware fields (ADR-07).

**Neutral**
- `recipeDigest` changes for any component moved to `nix` — by design; a builder switch is a new recipe.

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

Current module evidence in `pkg/nkd/recipe/nix_test.go`, `pkg/nkd/recipe/prebuilt_test.go`, and
`pkg/nkd/pipeline/pull_test.go` is partial. Acceptance requires an offline least-privilege execution of one real
Nix component and a test proving that byte-preserving acquisition has no profile while a deterministic
repackaging step changes the recipe graph and binds its output to the measured builder profile.
