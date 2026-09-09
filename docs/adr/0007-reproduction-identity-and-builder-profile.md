# ADR-07 — Reproduction identity & builder profile

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-07 in the consolidated NKD ADR set.
- **Relates to:** ADR-10 (pluggable builder types), ADR-04 (signing & verification), ADR-05 (trust object model /
  ValidationVector), ADR-08 (bake-off gate).

- **Scope:** This is the qualified reproduction contract. V1 production scope is explicit in §7.

## Context

NKD’s value rests on a precise, defensible meaning for **“Reproduced.”** The SRD scopes byte-for-byte
reproducibility to a single tuple — the same recipe, build engine, input manifest, and **builder profile** — and
requires the builder profile to be **immutable, identified by a signed digest**, with a **fail-closed preflight**
that aborts *before inputs resolve* when the environment does not satisfy output-affecting profile requirements.
The SRD also keeps **one publisher-qualified reference profile** while requiring each recipe to permit **at least
one operator-controlled profile**.

Pluggable builder types (ADR-10) change what the profile must describe: with Nix as a builder, the profile has to
pin the Nix version and its sandbox/privilege configuration, and the reproduction identity has to record which
builder a component was actually built under. This ADR defines the reproduction identity, the builder-profile
model, how profiles are matched and signed, and how profile identity scopes across a recipe that mixes builder
types and acquisition lanes.

## Decision

### 1. Reproduction identity

A reproduction claim is anchored to the tuple **`{ recipe identity, builder-profile identity, engineClass,
resolved-input-manifest }`**. Only `recipe identity` and the resolved inputs enter the Recipe Digest; the others
are trust and observation metadata and are never inputs to it (IR-purity).

- **`engineClass`** is the **observed** build substrate a component was actually built under — one of
  `{ oci-hermetic, nix, prebuilt-acquire }` — **per component**, recorded in the **detached build record** and
  **verified equal** to the builder the recipe declared. A mismatch — for example a recipe that declares `nix`
  whose artifact was produced on an unsandboxed or native lane — is a **different build**, not a sanctioned
  variant.
- Because it is an observation, `engineClass` is excluded from the recipe identity by construction.

### 2. Builder-profile model

A builder profile is an immutable, digest-identified descriptor that declares:

1. the **builders it provides**, each with pinned version and configuration (for `nix`: the Nix version and the
   sandbox/privilege set from ADR-10; for `oci-hermetic`: the runtime identity and image-pinning discipline);
2. its **output-affecting attributes**, each classified **fail-closed** or **advisory**, in two tiers:
   - **Builder-identity attributes** — the builder versions, container-runtime identity, the sandbox/privilege
     set, the source-date policy, and the **from-source `nkd-runner` digest** (ADR-18) — are **fail-closed by
     classification**, because they are *definitionally* output-affecting. No empirical demonstration is required
     to justify treating them as fatal.
   - **Host-environment attributes** — kernel, libc, microarchitecture — remain **evidence-gated**: advisory
     until a two-machine bake-off (ADR-08) demonstrates they change output. (Architecture is already
     demonstrated and is therefore fail-closed.)

Profile fields are also classified by **how they can be measured**:

- **Observable host facts/capabilities** are facts the preflight process can measure without fetching or
  executing build inputs: architecture, kernel/libc constraints, availability and configured isolation mode of
  the host container runtime, and required host capabilities.
- **Execution identities** are expected identities of artifacts or executables that participate in execution:
  the exact `nkd-runner` bytes, the bootstrap toolchain image **when that execution actually bootstraps the
  runner**, the component toolchain image, the Nix executable and realized closure, and the sandbox configuration
  actually used. They are verified at their execution seam, never as pseudo-host observations. The expected
  runner identity and any applicable bootstrap identity come from the profile (T2); expected component toolchain
  and closure identities come from the recipe/catalog lock (T1). Verification reports them together without
  duplicating either authority's pin.

`profile repin` changes expected execution identities. It MUST NOT make those identities appear to be values that
generic host observation can discover.

### 3. Two-stage fail-closed preflight

Preflight has two explicit stages and no ambiguous “missing observation” fallback:

1. **Plan/host preflight, before network or source resolution.** Authenticate and validate the profile, determine
   the recipe's required builder capabilities, and compare every fail-closed **observable host fact/capability**.
   A mismatch, or an unobservable field classified as a host fact, aborts with a distinct coded error before any
   fetch begins.
2. **Execution-identity preflight, after trusted inputs are materialized but before artifact-producing work.**
   Hash the runner binary that will execute; inspect the actual component image manifest/toolchain, the
   configured sandbox, and (for Nix) the realized offline closure; and compare each against its owning expected
   pin — the profile for the runner, the recipe/lock for component tools. The observation also records the
   runner's origin. If this execution bootstrapped the runner, it measures and verifies the bootstrap toolchain;
   if the exact runner bytes were preexisting, bootstrap identity is neither required nor permitted in the
   receipt. A missing measurement or a mismatch fails closed. No recipe artifact is built until this stage
   succeeds.


The split preserves early rejection for locally observable incompatibilities without pretending that the digest
of a not-yet-built runner or a not-yet-pulled toolchain is a host fact. Both stages are mandatory for a
“Reproduced” verdict.

Stage two returns an **opaque `ExecutionReceipt`**, not a boolean or a bag of caller-provided strings. The receipt
binds the authenticated profile digest, the stage-one host-observation binding, the exact selected build-node
definitions, and the canonical execution observation. Artifact-producing APIs revalidate that receipt against
their selected nodes before work; each result is explicitly `qualified-execution`, `profile-free-acquisition`, or
`unqualified`. Terminal verification re-hashes the artifact and exact-matches every from-source result to the
receipt before a report can authorize a ValidationVector or a reproduction claim.

The stock local qualified adapter implements executable-byte authority **on Linux only**. It captures the
already-mapped runner and the selected container-runtime executable as anonymous sealed capabilities, hashes
those capabilities, and executes that same held runtime capability for observation, build, and cleanup. It never
measures one pathname and later executes another. macOS has no equivalent `fexecve`/`execveat` primitive and its
descriptor filesystem is not executable, so the local qualified lane fails there with an engine-capability error
rather than falling back to a replaceable snapshot pathname. The configured container daemon, its protected
socket, the host kernel, and root-owned loader/library state remain explicit trusted-computing-base dependencies;
an executable digest alone does not identify that whole closure.


### 4. Signing & authentication

The builder profile is **detached-signed**. Under the signature-required verification mode, verify authenticates
the profile’s digest against a **pinned trusted identity**; an **unsigned, untrusted, or mismatched profile
cannot back a “Reproduced” verdict**, and fails with a distinct code. The profile’s signer need not be the
release signer — a distinct “builder-profile” identity is permitted and resolves through the same trust store as
any other signer (operator policy).

### 5. Capability-based matching; reference and operator-controlled profiles

A recipe declares its **builder requirements** — which builders it needs and acceptable version ranges — **not** a
fixed list of blessed profiles. Any profile that **provides** those builders and satisfies the fail-closed
attributes **qualifies**. The publisher’s single **reference profile** provides the qualified builder set, and
because matching is capability-based an operator can always author a conforming **operator-controlled** profile.
That satisfies both SRD clauses (one reference profile, and ≥1 operator-controlled profile per recipe) without
pinning operators to a specific profile identity.

### 6. Profile identity scope across mixed recipes

A recipe may mix from-source (`oci-hermetic`/`nix`, profile-bound) and `prebuilt` (host-independent) components.

- There is **one** builder profile per build environment, covering **all** from-source components — it provides
  every builder they need. The **profile identity binds only the from-source subset** of the expected-digests
  manifest.
- **Prebuilt components are profile-independent only when acquisition is byte-preserving.** Fetching and
  digest-verifying the declared bytes adds no profile identity.
- Repackaging, archive normalization, format conversion, patching, signing that changes bytes, or any other
  transformation of a prebuilt input is **artifact-producing build work**. It becomes a separate builder node
  whose transformation inputs and toolchain are pinned in `recipeDigest`, and its output binds to the builder
  profile and build record. Calling such work “acquisition” is prohibited.
- An **all-prebuilt recipe has no profile identity at all** and is fully host-independent.

### 7. Scope of reproduction

The two-stage profile protocol is required for every qualified reproduction claim. A pipeline-attested wrapped
build (ADR-10) records declared runtime, network, and privilege without builder-profile preflight or a
reproduction claim. Neither a helper-image pin nor self-reported isolation may mint the missing stage-two
receipt. Qualified helper-image admission remains unavailable until exact executable and runtime observations
satisfy this protocol; delivery by image is not proof that the helper reproduced.

## Consequences

**Positive**
- “Reproduced” has an exact, verifiable meaning bound to a signed profile; a substrate mismatch is caught as a
  different build rather than silently accepted.
- Capability-based matching keeps operators in control while preserving a single publisher reference profile.
- The model absorbs new builder types without schema churn — a new builder is a new entry in “provided builders”
  plus its output-affecting attributes.

**Negative / costs**
- The profile grows builder-specific fields; adding a builder means classifying its attributes.
- A recipe using `nix` raises the operator bar: their profile must provide Nix at the pinned version and
  privilege set.
- The two-tier classification requires a documented, defended list of which attributes are fail-closed by
  definition versus evidence-gated.
- A builder lane stays unavailable for qualified use until its adapter can measure all of its actual execution
  identities; a declared Nix lock digest cannot stand in for observing the realized closure.
- Exact held-executable invocation is Linux-specific. Another platform requires an equally strong
  execution-capability primitive or a separately authenticated producer protocol, not a named-file fallback.

**Neutral**
- `engineClass` and the observed host facts live only in the detached build record; the recipe identity is
  unchanged by them.

## Alternatives considered

- **Bind reproduction to the recipe identity alone** (no profile). Rejected — it makes the byte-identity claim
  host-independent, which is not achievable and would be dishonest.
- **Fixed per-recipe list of acceptable profiles.** Rejected — it makes the operator-controlled-profile guarantee
  hard and couples recipes to specific profile identities.
- **Mark every host attribute fail-closed.** Rejected — over-tightens, mass-halts operators on benign host
  variation, and contradicts “fail-closed only where warranted” for host-environment attributes.
- **Mark every attribute advisory until demonstrated.** Rejected for builder-identity attributes — they are
  definitionally output-affecting and should not require a demonstration to be treated as fatal.

## Follow-ups

- **ADR-08 (bake-off):** provides the empirical evidence that graduates a host-environment attribute from
  advisory to fail-closed, and covers `nix` components.
- **ADR-05 (trust objects):** the expected-digests manifest / ValidationVector binds the profile identity for the
  from-source subset and carries none for an all-prebuilt recipe.
- **ADR-04 (signing):** supplies the detached-signing and trusted-identity mechanism the profile signature uses.
- **ADR-18 (runner/bootstrap):** exposes the execution-identity measurements stage 2 consumes and prevents
  artifact nodes from starting before they pass.

## Acceptance evidence

Current module evidence in `pkg/nkd/profile/execution_test.go` and
`pkg/nkd/pipeline/execution_identity_integration_test.go` covers opaque host/execution receipts, exact
profile/node binding, conditional bootstrap identity, zero/changed/extra-input attacks, explicit report
qualification, concurrent validation, and the local stage-two barrier after authenticated source materialization.
The Linux local OCI adapter measures sealed runner/runtime capabilities, the selected image manifest, the runtime
endpoint, and the daemon server identity. Qualified local execution on other platforms, qualified Nix, and
generic CI execution remain fail-closed because no equally strong executable capability, realized-closure
observer, or provider-selected runtime receipt exists; these tests are not end-to-end provider conformance.

Acceptance additionally requires:

1. a `profile repin` → plan preflight → runner/toolchain materialization → execution preflight test proving a
   valid repin succeeds rather than failing as an unobservable host field;
2. negative tests for runner-byte, image-manifest, Nix executable, and sandbox mismatches that prove no artifact
   node starts;
3. cancellation tests for both stages with no leftover process/container; and
4. paired fixtures proving byte-preserving prebuilt acquisition is profile-free while repackaging the same input
   introduces a builder node and profile-bound output.
