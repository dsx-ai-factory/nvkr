# ADR-01 — Canonical serialization & digest-addressing

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-01 in the consolidated NVKR ADR set.
- **Relates to:** every digest-addressed object — the Build Digest (ADR-02), the trust objects (ADR-05),
  provenance and SBOM (ADR-09, ADR-13), publication and status (ADR-06). ADR-03 defines the error contract for
  serialization failures.

## Context

Many NVKR objects are identified and trusted by digest: the Build Digest, the ValidationVector, the ReleaseSet,
the evidence bundle, provenance, SBOMs, and status declarations. For each, the byte form is the identity, frozen
the moment it is published or signed. An auditor must be able to recompute that digest independently, in any
language, without access to NVKR's implementation.

Three requirements follow: one canonical serialization, so the same object always yields the same bytes; a
disciplined way to evolve schemas without invalidating already-signed bytes; and a way to keep the digest of one
kind of object from being mistaken for another.

## Decision

### 1. One canonical codec for every structured semantic identity

Every structured semantic identity — **including the Build Digest** — serializes through **one** canonical
codec: the **RFC-8785 (JSON Canonicalization Scheme) structural/string profile with NVKR's integer-only numeric
profile**. That is UTF-8, object keys sorted by UTF-16 code units, ECMAScript string escaping, no insignificant
whitespace, and object-owned slices pre-sorted before marshaling. **No host, clock, random, or
generated-identifier value** enters the output.

NVKR numeric values are JSON integer tokens only. Decimal fractions and exponent notation are rejected. Integer
tokens are preserved and rendered exactly at arbitrary precision, never converted through binary floating point;
independent implementations therefore MUST NOT route an integer through IEEE-754 before hashing. A schema that
needs a fractional quantity encodes a scaled integer or a canonical string, or introduces a future explicitly
versioned numeric profile.

There is **no grandfathered exception**: the Build Digest uses the same codec as everything else.
Human-authored **inputs** (the catalog) stay in a human-friendly format; only the **hashed** forms are canonical
JSON.

Raw file/blob integrity is a distinct layer. `sha256(exact-bytes)` identifies the bytes carried by a transport
descriptor and does not pretend to be a structured NVKR object. A filesystem tree or source closure is identified
by a named, versioned canonical manifest whose sorted entries bind normalized path, entry type, mode, symlink
target, and/or the raw content digest. A raw digest may be a field inside a typed semantic object, but it never
substitutes for that object's domain-separated identity or its authenticated receipt.

### 2. Versioning, strict dispatch & additive migration

- Every structured digest-addressed object carries **`schemaVersion`**. JCS orders object members
  lexicographically, so field position has no meaning and implementations MUST NOT require `schemaVersion` to
  appear first.
- A reader performs **strict version dispatch before interpreting semantic fields**: it extracts
  `schemaVersion`, selects the exact version-specific schema/decoder, rejects a missing or unsupported major
  version, and rejects unknown fields for that version. It MUST NOT decode an unknown version into the newest
  known structure or silently ignore fields.
- **Additive** evolution: a new field is optional and serializes to nothing when unset, so an object that does
  not set it is byte-identical to the pre-field version. A schema can gain fields without perturbing
  already-signed bytes.
- **Breaking** evolution — a type change, a field removal, or a changed meaning of an existing field — is a **new
  major schema identity within the object's frozen family domain**. A supported legacy decoder applies its
  original rules; otherwise old objects are rejected. No silent reinterpretation or signed-byte conversion is allowed.
- A `schemaVersion` bump therefore **deliberately changes the digest**: a schema change is a new identity.
- Golden-bytes tests pin each object's **valid minimal** and fully populated forms, so any accidental reordering
  or format drift is caught. Intentionally invalid zero values get fail-closed tests, not impossible golden
  output.

### 3. Domain-separated digests

The NVKR rebrand allocates the `nvkr.*` family namespace and `nvkr.dev` schemas
as a breaking framework-namespace cutover. It does not mutate historical `nkd.*`
identities or signed bytes. Unsupported old namespaces fail strict dispatch;
there is no implicit alias or conversion. Reserved names below are reserved
within the NVKR namespace. Historical allocations remain historical. See the
[cutover contract](../nvkr-rebranding.md) for producer and runtime boundaries.

Every structured semantic object's digest is computed with a frozen **family-kind tag** and an unambiguous
separator: `Digest(kind, obj) = sha256(UTF8(kind) ∥ 0x00 ∥ canonical-bytes)`. `schemaVersion` stays inside the
canonical bytes, so a major schema bump changes identity without changing the stable family tag. The tag
registry never reuses a tag across object families. An incompatible evolution that creates a new semantic object
family — one whose cross-major substitution must be distinguishable before schema dispatch — registers a new
family tag rather than overloading an existing one.

This prevents the digest of one family (a ValidationVector, say) from being accepted as another (a ReleaseSet)
even before the bytes are deserialized. Strict schema dispatch then prevents an unknown or incompatible major
within the same family from being interpreted.

### 4. On-disk form, display rendering, and the output contract

- The **canonical form of a structured identity object is what is stored on disk / on the wire**, so object file
  bytes equal canonical bytes equal the bytes covered by its semantic identity. There is no "the file is
  pretty-printed but the identity is the compact form" ambiguity.
- A reader of a digest-addressed object reserializes the strictly decoded object with the selected version's
  canonical codec and requires **byte-for-byte equality with the supplied bytes before accepting its digest or
  signature**. Alternate whitespace, key order, duplicate keys, unknown fields, or noncanonical scalar forms fail
  closed; canonicalization is not a lossy repair step at a trust seam.
- A separate **`--pretty` view** may render an object for humans. It is **display-only and never hashed**.
  Run Plan inspection validates the complete canonical source before producing text, pretty JSON, graph
  projections or semantic comparisons. These descriptive outputs cannot replace a canonical Plan or restore
  execution/evidence authority. The exact `run plan --json` result has no appended whitespace.
- **Result streams** (canonical objects) go to **stdout** or `--out` files; **logs and diagnostics** go to
  **stderr**; `--output json` emits **exactly one** canonical document. `stdout` never carries a log line, so
  results are always machine-consumable.

### 5. Public lifecycle schemas

The six public objects are Definition, Environment, Run, Artifact, Release, and Verification Policy. The
compiler-derived logical build graph uses the new **`nvkr.build`** family. **`nvkr.recipe` is reserved and
never minted**; historical `nkd.recipe` identities remain unsupported. Removing authored
Recipes does not rename, convert, or reauthenticate historical signed bytes. Cluster-definition,
cluster-composition, cluster-realization and staging-manifest families are likewise reserved and cannot mint
new identities. The removed compiler-handoff and copied execution-settings families are also reserved and
reject new minting, as do the removed redundant lifecycle-binding and qualified CI bundle (`nvkr.bundle`)
families. The unused qualified clearance implementation is removed: reproduction-result,
compatibility-result, conformance-result, evidence-waiver, clearance, status-trusted-time, publication,
publication-manifest and vulnerability-report allocations likewise remain reserved and reject minting. Retained publication-root and
release-content identities continue to serve the maintained publication path. Deployment topology and
classification have no current authored or executable schema.

The authored schemas use `apiVersion`; generated structured records use `schemaVersion`. The migration's
changed shapes are:

| Object | Schema |
|---|---|
| Definition | `nvkr.dev/definition/v5` |
| Component catalog and snapshot | `nvkr.dev/catalog/v3`, `nvkr.dev/catalog-snapshot/v3` |
| Component catalog overlay | `nvkr.dev/catalog-overlay/v3` |
| Executable and logical build graph | `nvkr.dev/ir/v4` |
| Run plan and artifact graph | `nvkr.dev/run-plan/v5`, `nvkr.dev/artifact-graph/v5` |
| Run and Release views | `nvkr.dev/run/v4`, `nvkr.dev/release/v4` |

Definition v5 retains removal of cluster views and adds an explicit required platform set for an aggregate OCI image. Catalog v3 removes delivery categories and node-role affinities. IR v4
binds typed VM disk, producer, selected toolchain-host platform,
public-variable and material-edge inputs into Build Digest. Old
major versions fail closed; the migration does not reinterpret or repair existing signed records.

Run plan v5 embeds the admitted Definition and Environment directly. The compiler owns complete output,
package, route and evidence binding; there is no separately versioned compile specification or execution-settings
document. Producer scheduling records reference the authoritative embedded IR instruction and cannot carry a
second copy of its runner, network, container, platform, execution requirements or class binding. Artifact graph
v5 retains that reference contract and freezes every native child build, producer result and evidence slot for an image index. Required output slots remain separate from observations.

The logical image-index build projection uses `nvkr.dev/image-index-build/v1` inside the existing
`nvkr.build` family: sorted `requiredPlatforms` plus sorted children containing only `platform`, `buildDigest`
and `component`. It is distinct from native IR v4 and from OCI manifest observations. Native IR bytes and
binary-bundle v1 remain unchanged. Publication-root metadata and media use v4 (ADR-20); Definition v4,
Plan/artifact-graph v4 and Run/Release v3 are rejected rather than reinterpreted with aggregate membership.

Production SLSA build-type URIs use `/v2` for the replacement of compiler-handoff parameters by the original
`definitionDigest` and `environmentDigest`. SLSA provenance/v1 and in-toto Statement/v1 remain unchanged.
Historical production predicates are rejected rather than relabeled or re-signed under the current contract.

Environment stays `nvkr.dev/environment/v1`; the generated Run's unchanged exact-file commit manifest stays
`nvkr.dev/generated-run/v1`. Unchanged wire shapes keep their existing major. Evidence,
publication, and release owners version every field or meaning changed from Recipe identity to Build identity;
they must not accept a former `recipeDigest` under a new `buildDigest` spelling. Existing family tags remain
frozen even where their names contain a version; the schema inside the canonical bytes distinguishes changed
major versions within a family. Run presentation and Release reference views do not acquire redundant identities
without a canonical subject.

M3 qualification evidence (ADR-16) registers the following distinct schema and family. This is
pipeline-attested build/evidence qualification; it does not reuse a reserved qualified-clearance family.

| Object | Canonical schema | Frozen family kind | Attestation predicate type |
|---|---|---|---|
| Qualification evidence | `nvkr.dev/qualification-evidence/v2` | `nvkr.qualification-evidence` | `https://nvkr.dev/qualification-evidence/v2` |
| Qualification policy | `nvkr.dev/qualification-policy/v1` | `nvkr.qualification-policy` | Independently selected configuration; no evidence predicate |
| Native smoke worker | `nvkr.dev/tool-smoke-worker/v1` | `nvkr.tool-smoke-worker` | Detached signed document; no qualification attachment |

The document binds the authenticated originating repository, Run, attempt, commit, workflow/ref and build
invocation; the separately authenticated qualification invocation; gate version; Definition, Environment,
Plan and relevant input identities; and complete exact output/subject membership with per-check results.
Object-owned output, subject and check collections have deterministic ordering. Overall output verdicts are
pass/fail only. Applicable checks must pass before publication; non-applicable checks carry the explicit
applicability result. External suites outside this MVP use `not-evaluated` with reason `outside MVP scope`;
this is never interpreted as passed. Disk scope states producer-only evidence and no content inspection.
Unknown fields, versions or verdicts, duplicate members, noncanonical bytes and inexact bindings fail closed.

The complete qualification-policy identity includes source and qualifier workflow selection, the distinct
same-repository native smoke workflow, every platform executable/image pin, fixed tool profiles, explicit
non-tool output scope and external-suite requirements. Local executable paths are locators, excluded from
this identity; exact executable byte pins remain included. Object-owned set collections are sorted before
hashing. The existing release Verification Policy digest keeps its separate meaning.

A native smoke-worker document binds the full artifact-check description, exact source and qualifier
invocations, complete qualification-policy identity, measured native platform/runtime/image and exact
per-member byte identities, expected and observed versions and fixed command profiles. It carries no
qualification signing authority. The final owner authenticates the distinct worker certificate identity,
issuer, exact qualifier Run-attempt URI and top-level workflow configuration URI/commit from the same
verified certificate, then validates complete, disjoint member coverage. Generated transport names and
unsigned report fields never restore a smoke receipt. These registrations state the wire contracts;
reconciliation separately records runtime, worker authentication and hosted acceptance implementation.

The M4 release-manifest document remains deferred and requires its own registered canonical schema and
domain-separated identity before implementation. Pretty-printed JSON hashes are insufficient for either
document. Reconciliation records implementation and external acceptance independently of schema registration.

## Consequences

**Positive**
- One codec means one place to reason about determinism and one recomputation path for any language.
- Additive migration lets trust objects evolve without breaking existing signatures.
- Domain separation removes a class of digest-confusion attacks.
- Result/log stream separation keeps outputs pipeline-safe.

**Negative / costs**
- Because the Build Digest uses the canonical codec, its identity is defined solely by this ADR: any codec
  change is a breaking change to every identity, so the codec is frozen once adopted.
- The integer-only numeric profile is deliberately narrower than full RFC-8785 number handling. Schemas needing
  fractions cannot add them without a versioned profile decision.

**Neutral**
- Human-authored catalog inputs stay in their existing human format; only hashed artifacts are canonical JSON.

## Alternatives considered

- **Freeze an existing pretty-printed JSON form as canonical.** Rejected — harder to recompute in other
  languages and locks in indentation.
- **Use a YAML codec everywhere.** Rejected — YAML is a poor cross-language canonical form.
- **Give the Build Digest a separate codec / grandfather it.** Rejected — two codecs to reason about, and the
  "avoid changing a computed digest" motive is implementation baggage, not a real pre-launch constraint.
- **CBOR / protobuf canonical encoding.** Rejected — non-human-readable and over-engineered for string-digest
  payloads.

## Follow-ups

- **ADR-02:** the Build Digest is the digest of the compiled build graph under this codec, subject to the purity
  boundary.
- **ADR-05 / ADR-09 / ADR-13:** the trust, provenance, and evidence objects each declare a `schemaVersion` and
  serialize through this codec; the evidence-serialization profile is this codec plus the set of declared schema
  versions.

## Acceptance evidence

Current module-level evidence is `pkg/nvkr/canonical/canonical_test.go` (golden JCS bytes, integer boundaries,
invalid UTF-8 rejection, immutable kind registration, and domain separation), `pkg/nvkr/ir/marshal_test.go`
(structurally transport-free, field-classified build identity), `pkg/nvkr/ir/identity_contract_test.go`
(independently calculated acquisition/source graph vectors, retired-family separation and old-field rejection), `pkg/nvkr/pipeline/source_snapshot_test.go`
(versioned canonical tree identity including empty directories, modes, content, and symlink targets). This is **contract evidence**, not
corpus-wide or independent-language acceptance.

Acceptance additionally requires:

1. table-driven valid-minimal and fully populated fixtures for every structured digest-addressed family/schema
   proving missing/unknown versions and unknown fields fail closed under version-specific dispatch; a new
   semantic family must additionally prove it received a distinct registered kind tag;
2. a negative corpus proving noncanonical-but-equivalent JSON, duplicate keys, and noncanonical numbers are
   rejected before signature/digest acceptance, including fractions/exponents, plus exact vectors for zero,
   signed boundaries, `uint64` maximum, and integers beyond IEEE-754 precision;
3. golden on-disk bytes proving `storedBytes == canonicalBytes == hashedBytes` for every signed object; and
4. the same public test vectors passing in at least one independent implementation.
