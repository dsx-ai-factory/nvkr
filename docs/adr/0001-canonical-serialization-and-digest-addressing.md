# ADR-01 — Canonical serialization & digest-addressing

- **Decision status:** Proposed (implementation-aligned draft; pending named-decider ratification)
- **Implementation maturity:** Tracked separately in `reconciliation.md`; implementation and test evidence do not
  constitute governance ratification.
- **Acceptance-test maturity:** Tracked separately in `reconciliation.md`; green unit tests alone do not
  establish end-to-end conformance.
- **Deciders:** [INPUT NEEDED — author + reviewers]
- **Index:** ADR-01 in the consolidated NKD ADR set.
- **Relates to:** every digest-addressed object — the Build Digest (ADR-02), the trust objects (ADR-05),
  provenance and SBOM (ADR-09, ADR-13), publication and status (ADR-06). ADR-03 defines the error contract for
  serialization failures.

## Context

Many NKD objects are identified and trusted by digest: the Build Digest, the ValidationVector, the ReleaseSet,
the evidence bundle, provenance, SBOMs, and status declarations. For each, the byte form is the identity, frozen
the moment it is published or signed. An auditor must be able to recompute that digest independently, in any
language, without access to NKD's implementation.

Three requirements follow: one canonical serialization, so the same object always yields the same bytes; a
disciplined way to evolve schemas without invalidating already-signed bytes; and a way to keep the digest of one
kind of object from being mistaken for another.

## Decision

### 1. One canonical codec for every structured semantic identity

Every structured semantic identity — **including the Build Digest** — serializes through **one** canonical
codec: the **RFC-8785 (JSON Canonicalization Scheme) structural/string profile with NKD's integer-only numeric
profile**. That is UTF-8, object keys sorted by UTF-16 code units, ECMAScript string escaping, no insignificant
whitespace, and object-owned slices pre-sorted before marshaling. **No host, clock, random, or
generated-identifier value** enters the output.

NKD numeric values are JSON integer tokens only. Decimal fractions and exponent notation are rejected. Integer
tokens are preserved and rendered exactly at arbitrary precision, never converted through binary floating point;
independent implementations therefore MUST NOT route an integer through IEEE-754 before hashing. A schema that
needs a fractional quantity encodes a scaled integer or a canonical string, or introduces a future explicitly
versioned numeric profile.

There is **no grandfathered exception**: the Build Digest uses the same codec as everything else.
Human-authored **inputs** (the catalog) stay in a human-friendly format; only the **hashed** forms are canonical
JSON.

Raw file/blob integrity is a distinct layer. `sha256(exact-bytes)` identifies the bytes carried by a transport
descriptor and does not pretend to be a structured NKD object. A filesystem tree or source closure is identified
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
- **Result streams** (canonical objects) go to **stdout** or `--out` files; **logs and diagnostics** go to
  **stderr**; `--output json` emits **exactly one** canonical document. `stdout` never carries a log line, so
  results are always machine-consumable.

### 5. Public lifecycle schemas

The six public objects are Definition, Environment, Run, Artifact, Release, and Verification Policy. The
compiler-derived logical build graph uses the new **`nkd.build`** family. **`nkd.recipe` remains a reserved
historical allocation**; active graph compilation never mints new identities under it. Removing authored
Recipes does not rename, convert, or reauthenticate historical signed bytes. Cluster-definition,
cluster-composition, cluster-realization and staging-manifest families are likewise reserved and cannot mint
new identities. Deployment topology and classification have no current authored or executable schema.

The authored schemas use `apiVersion`; generated structured records use `schemaVersion`. The migration's
changed shapes are:

| Object | Schema |
|---|---|
| Definition | `nkd.dev/definition/v4` |
| Component catalog and snapshot | `nkd.dev/catalog/v3`, `nkd.dev/catalog-snapshot/v3` |
| Component catalog overlay | `nkd.dev/catalog-overlay/v3` |
| Executable and logical build graph | `nkd.dev/ir/v3` |
| Internal compile specification, Run plan, artifact graph | `nkd.dev/compile-spec/v3`, `nkd.dev/run-plan/v3`, `nkd.dev/artifact-graph/v3` |
| Run and Release views | `nkd.dev/run/v3`, `nkd.dev/release/v3` |
| Internal lifecycle binding | `nkd.dev/lifecycle-binding/v3` |
| Low-level execution bundle and manifest | `nkd.dev/execution-bundle/v2`, `nkd.dev/execution-bundle-manifest/v2` |

Definition v4 removes cluster views. Catalog v3 removes delivery categories and node-role affinities. IR v3
binds effective AICR bundle-rendering settings, including the explicit Helm default, into Build Digest. Old
major versions fail closed; the migration does not reinterpret or repair existing signed records.

Environment stays `nkd.dev/environment/v1`; the generated Run's unchanged exact-file commit manifest stays
`nkd.dev/generated-run/v1`. Unchanged wire shapes keep their existing major. Evidence,
publication, and release owners version every field or meaning changed from Recipe identity to Build identity;
they must not accept a former `recipeDigest` under a new `buildDigest` spelling. Existing family tags remain
frozen even where their names contain a version; the schema inside the canonical bytes distinguishes changed
major versions within a family. Run presentation and Release reference views do not acquire redundant identities
without a canonical subject.

The deferred v1 qualification-evidence and release-manifest documents (ADR-16) require strict schema dispatch
and registered, domain-separated identities before implementation. Pretty-printed JSON hashes are insufficient.

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

Current module-level evidence is `pkg/nkd/canonical/canonical_test.go` (golden JCS bytes, integer boundaries,
invalid UTF-8 rejection, immutable kind registration, and domain separation), `pkg/nkd/ir/marshal_test.go`
(structurally transport-free, field-classified build identity), `pkg/nkd/ir/identity_contract_test.go`
(independently calculated acquisition/source/AICR graph vectors, retired-family separation and old-field rejection), `pkg/nkd/pipeline/source_snapshot_test.go`
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
