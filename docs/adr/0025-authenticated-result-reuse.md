# ADR-25 — Authenticated result reuse

- **Decision status:** Proposed (design only; pending named-decider ratification)
- **Implementation maturity:** Designed, not implemented. No reuse key, discovery, acceptance, receipt or Run
  binding exists; see `reconciliation.md`.
- **Acceptance-test maturity:** None. The test matrix below is the acceptance target for the implementing unit;
  no contract test, hosted Run or qualification result exercises reuse.
- **Deciders:** Pending; release-engineering, publication and qualification owners must be named before ratification.
- **Index:** Proposed ADR-25 supplements ADR-01–22 and proposed ADR-23/24; it remains outside the consolidated
  contract set until accepted.
- **Relates to:** ADR-01 (canonical codec and digest families), ADR-02 (Build identity tiers), ADR-04 (signing and
  consumer verification), ADR-06 (status), ADR-09 (production provenance), ADR-11 §2 (independent output uses),
  ADR-13 (typed evidence), ADR-16 §4–§5 (incremental validation and evidence tiers), ADR-17 §5–§6 (receipt lifetime
  and immutable attempts), ADR-18 §7–§8 (terminal verify, execution graph and partitions), ADR-19 (output
  membership), ADR-20 §6 (pipeline-attested publication and same-Run retries), ADR-21 (generated bundles).
- **Scope:** Cross-Run reuse of a complete, previously published named output in the `pipeline-attested` lane:
  eligibility, discovery, authenticated acceptance, refusal and binding into the current Run. Qualified reuse,
  local static-unqualified reuse, cross-output deduplication and reuse of qualification verdicts are excluded.

## Context

A full NVKE development Run executes several hundred operations for 69 named outputs, even when most outputs'
inputs are unchanged since the previous published Run. Execution partitioning (ADR-18 §8) reorganizes that work
but performs all of it. Producer cost should be proportional to change.

Existing contracts already fix the boundary. ADR-11 §2, ADR-17 §6 and ADR-19 §1 make each named output an
independent required use: equal Build Digests do not merge producer jobs, results or evidence. ADR-16 §4 says a
declaration diff identifies only reuse candidates, and ADR-18 §7 requires any future reuse to authenticate a prior
result bound to the exact output, members, policy, status and time. The only admitted reuse today is ADR-20 §6's
same-GitHub-Run partial retry, which consumes an authenticated output-local root from an earlier attempt of the
same run, Plan and workflow.

The Build Digest is necessary but insufficient as a reuse key. It omits the output name, packaging, the
builder-profile identity, execution bindings, evidence requirements and the signer policy (ADR-02 T2), all of which
affect the delivered bytes or what a consumer may accept. A cache is also an untrusted store: anything that finds
an entry can be stale, truncated, substituted or written by another workflow.

## Decision

### 1. Unit of reuse and invariants

The unit of reuse is one complete **named output** in one Run: its native producer uses (for an aggregate image,
every `(output, platform)` child plus the aggregate package), terminal observations, payload, production
provenance and required SBOM/evidence closure. Reuse is atomic per output. An aggregate either reuses every
native child and the aggregate from one prior publication or executes all of them; a partly reused platform set
is refused.

These invariants are not negotiable by policy:

1. **One Definition, one complete Plan, one logical Run, one Release.** Reuse changes which operations execute,
   never the required output set, Plan identity, product identity or release authority. Execution partitions see
   one frozen reuse decision set; partitioning cannot change any reuse decision.
2. **No sibling reuse.** A result for output `A` never satisfies output `B`, in the same or another Run, even when
   their Build Digests, payload digests and producers are equal. No intra-Run deduplication is introduced.
3. **No invented execution.** A reused output is recorded as reused, with its original origin. Nothing signed or
   recorded by the current Run claims that the current Run executed, observed or attested the prior producer work.
4. **Discovery is not acceptance.** Finding a candidate grants nothing. Only the owning module's authenticated
   acceptance produces a reuse receipt, and only that receipt can bind a reused output into the current Run.
5. **Re-authenticate from bytes every time.** No cache-side "valid" flag, negative entry, index field or earlier
   acceptance carries authority into a later Run.

### 2. Reuse eligibility: the reuse key

The compiler derives one **reuse key** per named output from the current complete, validated Plan. It is a
structured identity hashed through the ADR-01 codec under a new family, proposed as `nvkr.reuse-key` with schema
`nvkr.dev/reuse-key/v1`; the family is registered in ADR-01 only when implemented. The key binds:

| Field | Source | Why |
|---|---|---|
| Plan schema and reuse-key schema | Plan / this ADR | Unknown or changed schema majors cannot be compared |
| Named output and artifact format | Definition via Plan | Prevents sibling and format substitution |
| Required target platform set, and per child `(platform, component, native Build Digest)` | Plan | Exact build inputs per ADR-02 T1, including source pins, builder type, toolchain pins and platform |
| Aggregate Build Digest when applicable | Plan | ADR-02 aggregate identity |
| Byte-changing packaging relationships | Definition via Plan | Excluded from Build identity but changes delivered bytes |
| Builder-profile identity, producer execution class and execution binding | Plan (ADR-02 T2, ADR-18) | Toolchain execution environment and provenance builder semantics |
| Generated helper and evidence-tool pins (SBOM generators, cosign) | Plan / lock | Evidence produced by a different tool is a different evidence result |
| Required evidence set for the output | Plan | A prior entry with fewer evidence kinds cannot satisfy more |
| Evidence lane | Environment via Plan | Lanes never substitute (ADR-16 §5) |
| Publication mode, repository, workflow identity and production/development class | Environment via Plan | Origin admissibility; development never satisfies production or the reverse |
| Signer identity and issuer policy | Environment | The prior output must verify under the current signer policy |

The key excludes the Run ID, attempt, timestamps, runner hostnames, transport locators and registry hosts. Any
unrecognized or missing field makes the output ineligible. Equality of two reuse keys is necessary for
eligibility and never sufficient for acceptance (§3).

Reuse is opt-in. A Run attempts reuse only when its Environment selects an explicit reuse policy (proposed
field `resultReuse`, default disabled) with a positive maximum entry age. The policy is bound into Plan identity,
so `run generate --check` stays byte-exact; the cache's contents never affect generated bytes.

### 3. Discovery and authenticated acceptance

**Discovery** maps a reuse key to a bounded, deterministically ordered list of candidate immutable publication
references (`name@sha256:…` output-local roots under ADR-20 §6). A discovery source is untrusted transport: a
registry listing, a prior Run's retained records or a future index document. Discovery is bounded in count, bytes
and time, never writes, and never resolves a tag as identity. Its result is a description; a forged, stale or
empty list can only cause refusal or execution.

**Acceptance** is performed by one owning module (proposed `pkg/nvkr/reuse`; the compiler owns key derivation)
for one candidate and one current output. It completes every step before issuing a receipt:

1. Authenticate the candidate root's signature and the output's artifact signature under the **current**
   Environment signer policy and the `pipeline-attested` consumer contract (ADR-04 §5).
2. Authenticate the root's exact membership, the prior Plan bytes it binds and the prior Run origin
   (repository, run ID, attempt, workflow builder) from signed metadata, not from discovery.
3. Recompute the reuse key from the authenticated prior Plan for the same output name and require byte-equality
   with the current key.
4. Retrieve and digest-check the complete payload and evidence closure (artifact-publication scope, not the
   payload-free authenticity scope), including every native child of an aggregate.
5. Validate the production provenance predicate semantically against the prior Plan and exact subjects (ADR-09)
   and verify every required SBOM bundle (ADR-13).
6. Check freshness: the signed production finish time of the prior output must be no older than the policy's
   maximum age relative to the current Run's **reference instant**, and not after it. The reference instant is
   the issued-at time of a GitHub Actions OIDC token that the admission step (§5) requests and verifies against
   the GitHub issuer for the current repository, workflow, run ID and run attempt; the runner clock and the
   provider's run metadata are not authority. If the step cannot obtain and verify that token, freshness cannot
   be established and every candidate is refused as `stale`. When an authenticated ADR-06 status checkpoint
   exists for the candidate publication, a withdrawn or non-current status refuses it.
7. Require exactly one surviving **original origin**. A candidate's original origin is the authenticated prior
   Run origin of its production provenance; a digest-preserving copy published by a later Run (§5) keeps that
   original origin. Survivors with the same original origin and byte-identical closure digests are one
   candidate; the lowest publication reference in byte order is the transport source. Survivors with different
   original origins, or with different closure digests, are a conflict even when their payload digests are
   equal.

Steps run in order and the first failing step assigns the refusal reason. Authentication (steps 1–2) always
precedes reading any prior claim, so a candidate that does not verify under the current signer policy is
`corrupt` even when its unauthenticated metadata would also show a different reuse key.

Success yields an opaque, process-local **reuse receipt** bound to the current Run owner, the named output, the
current reuse key, the candidate publication reference, the prior origin and the exact verified closure bytes.
It follows ADR-17 §5: it cannot be serialized, restored or constructed by a caller, and Run closure revokes it
and its copies. A decoded record, digest string or boolean cannot recreate it.

### 4. Refusal and invalidation

A refusal records a closed, typed reason and the output executes normally. Refusal never fails the Run by itself
and never falls back to a weaker check. Context cancellation and deadline expiry are errors, not refusals.
Failures after the binding in §5 fail the Run; they never revert silently to execution.

| Class | Condition | Reason |
|---|---|---|
| Disabled | Reuse policy absent or disabled | `disabled` (no discovery performed) |
| Miss | No candidate within bounds | `not-found` |
| Stale | Older than maximum age; production time after the reference instant; no verified reference instant; ADR-06 withdrawal or non-current status | `stale` |
| Incomplete | Missing native child, payload blob, provenance, SBOM or required evidence; root not finalized or signed; prior output failed | `incomplete` |
| Corrupt | Digest or size mismatch; noncanonical or undecodable bytes; signature under the current signer policy or provenance verification fails | `corrupt` |
| Incompatible | Authenticated reuse key differs (Build Digest, platform set, packaging, toolchain/profile, evidence set, lane, mode, origin, signer policy) or unknown schema | `incompatible` |
| Sibling | Candidate's authenticated output name differs from the current output | `incompatible` (never a lookup by Build Digest alone) |
| Conflict | More than one authenticated original origin, or different closure bytes, among surviving candidates | `conflict` |
| Over budget | Discovery or acceptance exceeds count, byte or time bounds | `bounded` |

Invalidation is implicit: there is no mutable cache state to repair. Any change to a keyed input changes the
key, and every acceptance repeats §3 from bytes. Deleting or retaining cache entries is an operational retention
choice outside authority.

### 5. Binding reused evidence to the current Run

Reuse decisions are made in one pre-execution admission step in the entry workflow, after Plan admission and
before any producer operation. The step emits a frozen, canonical **reuse decision set** listing, for every
required output, `execute` or `reuse` with the candidate reference and the typed refusal reason when executing.
The set binds the Plan digest, the current repository, workflow, run ID and run attempt, and the verified
reference instant (§3 step 6); every partition worker and the finalizer consume this exact set. It cannot change
after producers start.

One decision set serves exactly one GitHub run attempt, which ADR-17 §6 already makes one immutable NVKR Run.
A new attempt, including an ADR-20 §6 partial retry, repeats admission with its own reference instant and
freezes a new set; it never inherits an earlier attempt's decisions.

The decision set is a description, not authority, and the admission step's receipts end with its process. The set
carries the admission step's verified OIDC token, requested with an NVKR-only audience that no cloud trust policy
admits. Each receiving process verifies the current Plan digest and re-verifies the token's signature, issuer,
audience, repository, workflow, run ID and run attempt against the set and its own run, ignoring only token
expiry; any mismatch or verification failure fails the Run before producer work. A process that only omits a
reused output's operations needs nothing more. Any process that consumes a reused output's closure (a descendant
operation, Run record binding or publication copy) repeats §3 acceptance from bytes for exactly the recorded
candidate and output, evaluates step 6 at that token's issued-at instant rather than a new one, and holds its own
receipt, so a delayed re-acceptance reaches admission's freshness result. A re-acceptance that refuses, or that
selects a different candidate, fails the Run rather than reverting to execution, because the producer operations
have already been omitted.

For a reused output:

- **Execution graph.** Its producer, observation and per-output evidence operations do not run. The ADR-18 §8
  execution graph accounts them with a new distinct outcome, `reused`, never `succeeded` or `skipped`. A
  `reused` prerequisite satisfies only descendants that consume that output's accepted closure; a `skipped`
  result never counts as reused. Outcome accounting stays an exact disjoint cover.
- **Run record.** The output's slot records origin `reused`, the reuse key, the prior publication reference,
  prior Plan digest, prior Run origin and the accepted closure digests. Completeness requires every required slot
  to be satisfied by exactly one `executed` or `reused` result; the observed payload digest is the authenticated
  prior digest.
- **Provenance.** The original signed SLSA production predicate and SBOM bundles are retained byte-for-byte as
  that output's evidence. The current Run signs no production provenance for the reused output. Release assembly
  records its own current invocation plus a reuse statement binding each reused output to its prior origin, as
  ADR-09 already does for same-Run attempts.
- **Publication.** Reused payload and evidence blobs are copied by digest into the current Run's publication
  namespace before release assembly, so the Release does not depend on another Run's retention. The copy is
  transport; publication re-verifies the exact digests after the copy.
- **Lane and qualification.** The Release keeps its `pipeline-attested` lane. Qualification (ADR-16) verifies the
  complete current Release closure, including reused outputs; no qualification verdict, smoke result or check
  receipt is reused.

### 6. Lanes

- `pipeline-attested`: designed here.
- `qualified`: deferred. ADR-16 §4 already requires an authenticated prior clearance plus exact typed receipts
  rebound to the current member and policies; this ADR adds no qualified shortcut.
- `static-unqualified`: no reuse acceptance, because there is no authenticated producer evidence to accept.
  Local dry-run and local diagnostic Runs always execute.

## Consequences

**Positive**
- Producer cost can scale with change while every reused byte is re-authenticated under the current policy.
- The Release states truthfully which outputs the current Run executed and which it reused, from where.
- Partitioning and reuse stay orthogonal: one frozen decision set serves every partition.

**Negative / costs**
- Acceptance retrieves the complete payload closure, which costs registry bandwidth for large disk outputs, and
  every process that consumes a reused closure repeats that retrieval.
- Freshness requires the admission step to obtain a GitHub OIDC token, which the decision set retains so later
  processes reproduce the same reference instant.
- A new key family, Run slot origin, execution outcome and publication copy step must be implemented and
  versioned.
- Until production status discovery exists (ADR-06), staleness relies on the maximum-age bound.

**Neutral**
- The default stays full execution; no existing Run, bundle or Release changes meaning.

## Alternatives considered

- **Key on Build Digest alone.** Rejected: it permits sibling substitution and ignores packaging, evidence,
  toolchain profile, lane and signer policy.
- **Trust a cache index's verified flag or a provider cache hit.** Rejected: discovery is not authority.
- **Re-sign reused outputs as current production.** Rejected: it invents current execution and breaks ADR-09.
- **Reference prior namespaces without copying.** Rejected: the Release would depend on another Run's retention
  and cleanup (ADR-20).
- **Per-platform reuse inside an aggregate.** Rejected for now: atomic output reuse keeps completeness simple.
- **Fail the Run on any refusal.** Rejected: refusal is safe because the output executes under unchanged checks.

## Follow-ups

- **ADR-01:** register the reuse-key family and the reuse decision set schema when implemented.
- **ADR-17 / ADR-19:** add the `reused` slot origin to the Run record schema (new major).
- **ADR-18 §8:** add the `reused` execution outcome to the execution graph.
- **ADR-20:** specify the digest-preserving copy and the reuse statement in release assembly.
- **ADR-06:** production status discovery replaces age as the primary staleness control.

## Acceptance evidence

None yet. Backlog item 5 (incremental execution) implements against this decision. Its contract suite must cover
at least this matrix; each refusal row asserts the typed reason and that the output executes, and each error row
asserts the stable coded error.

| # | Case | Setup | Expected |
|---|---|---|---|
| 1 | Disabled by default | No reuse policy | No discovery call; every output executes; reason `disabled` |
| 2 | Exact hit | Prior signed root, same key, within age | Receipt issued; origin `reused`; producer operations accounted `reused` |
| 3 | Miss | Empty discovery | `not-found`; output executes |
| 4 | Sibling, equal Build Digest | Candidate for output `A` offered for output `B` with identical Build Digest and bytes | `incompatible`; `B` executes |
| 5 | Same-Run sibling | Two outputs in one Run with equal Build Digests | Both execute; no intra-Run deduplication |
| 6 | Source pin change | One source commit differs | Key differs; `incompatible` |
| 7 | Toolchain pin change | Builder or SBOM tool pin differs | `incompatible` |
| 8 | Builder-profile or execution-class change | Same Build Digest, different profile | `incompatible` |
| 9 | Packaging change | Same Build Digest, different packaging | `incompatible` |
| 10 | Platform change | Native platform or aggregate platform set differs | `incompatible` |
| 11 | Evidence set grows | Current Plan requires an extra evidence kind | `incompatible` |
| 12 | Lane mismatch | Candidate from another lane or a static-unqualified record | `incompatible`; qualified Runs never call acceptance |
| 13 | Mode mismatch | Development candidate in production, and the reverse | `incompatible` |
| 14 | Signer policy change | Candidate signed only under the old identity; separately, signed by an identity the current policy admits but bound to a different signer policy in its prior Plan | Old identity only: `corrupt` (step 1 precedes key comparison); admitted signer with different bound policy: `incompatible` |
| 15 | Stale by age | Production finish older than maximum age | `stale` |
| 16 | Future-dated | Production finish after current Run start | `stale` |
| 17 | Withdrawn | Authenticated status checkpoint withdraws candidate | `stale` |
| 18 | Missing native child | Aggregate root lacks one platform | `incomplete`; whole aggregate executes |
| 19 | Missing SBOM or provenance | Blob or bundle absent | `incomplete` |
| 20 | Unsigned or unfinalized root | Prepared but unsigned output root | `incomplete` |
| 21 | Payload digest mismatch | Registry returns altered bytes | `corrupt` |
| 22 | Truncated blob or size mismatch | Short read | `corrupt` |
| 23 | Noncanonical metadata | Reordered keys or extra whitespace in a signed record | `corrupt` |
| 24 | Provenance subject substitution | Predicate subject names another digest | `corrupt` |
| 25 | Unknown schema major | Prior Plan or key schema unknown | `incompatible` |
| 26 | Conflict | Two authenticated candidates with different payloads | `conflict`; output executes |
| 27 | Discovery over budget | Candidate count, bytes or time exceeds bounds | `bounded`; no partial acceptance |
| 28 | Forged discovery | Discovery claims verified; bytes fail checks | Refusal reason from checks; flag ignored |
| 29 | Caller-built receipt | Decoded record or struct passed as receipt | Rejected; only acceptance issues receipts |
| 30 | Receipt after Run close | Use receipt or copy after closure | Rejected; revoked |
| 31 | Cancellation | Context canceled during acceptance | Coded timeout/cancel error, not a refusal |
| 32 | Post-binding failure | Copy into current namespace fails or digest changes after copy | Run fails; no fallback to execution |
| 33 | Partition invariance | Same inputs, flat and partitioned at capacities 1, 2 and 64 | Identical decision set, outcome accounting and Release membership |
| 34 | Decision set frozen | Worker receives altered or missing decision set | Run fails before producer work |
| 35 | Skipped is not reused | Producer skipped by an upstream failure | Accounted `skipped`; slot incomplete; Run fails |
| 36 | Reused prerequisite | Descendant consuming reused closure | Runs with accepted closure; unrelated descendants unaffected |
| 37 | No re-signing | Inspect current Run signatures | No production predicate for reused outputs; reuse statement binds prior origin |
| 38 | Qualification | Qualify a Release with reused outputs | Full closure verified; no prior verdict or smoke result consumed |
| 39 | Generation determinism | Regenerate with different cache contents | Byte-identical bundle; `run generate --check` passes |
| 40 | Mixed Run | Some outputs reused, others executed | Complete Release; each slot exactly one origin |
| 41 | Same payload, different origins | Two authenticated candidates with equal payload digests from different original Runs | `conflict`; output executes |
| 42 | Copy of one origin | Original publication and a later Run's digest-preserving copy both survive | Receipt issued; lowest reference used for transport; original origin recorded |
| 43 | Fresh-process re-acceptance | Finalizer or consuming worker repeats acceptance in a new process; candidate bytes replaced between admission and re-acceptance | Run fails before binding; no fallback to execution |
| 44 | Decision set replay | Worker receives a valid set from another repository, workflow, run ID, attempt or Plan | Run fails before producer work |
| 45 | Reference instant | OIDC token missing, unverifiable or bound to another attempt; candidate exactly at and just past the age bound | Token failure refuses every candidate as `stale`; boundary candidate accepted, one past it `stale` |
| 46 | Retry attempt | Same run, new attempt after candidate ages past bound | New attempt freezes its own set; candidate refused `stale`; earlier attempt's set rejected |
| 47 | Delayed re-acceptance | Consumer re-accepts after the candidate has aged past the bound measured from a new token; separately, the set carries an expired admission token, a token from another attempt, or one with a non-NVKR audience | Freshness evaluated at the admission token's issued-at; candidate accepted despite token expiry; foreign-attempt or wrong-audience token fails the Run before producer work |

Rows 2, 32, 33 and 37–47 also need one hosted development Run before reconciliation may claim the path was
externally exercised. No qualified or production claim follows from these tests.
