# Portable test evidence

Portable observations bind exact input and tested output identities, independently selected suite
policy and required cases, invocation, public environment facts, execution and cleanup outcomes,
and an inventory of original approved result files. Authentication proves the selected attester's
recorded claim. It does not establish physical hardware, installed deployment bytes or current health.
The implementation follows the plan in `docs/session-plans-2026-10-12/11-portable-test-evidence-on-pr143.md`
in the primary checkout; [reconciliation](adr/reconciliation.md) owns current support and evidence.

## Emit and inspect locally

Prepare a canonical `testobservation.Observation` description with schema
`nvkr.dev/test-observation/v1`, an empty `files` inventory and zero `summary`. Each input, subject,
suite revision, image digest, selection, required-case list and policy digest comes from its owning
source or explicitly reviewed test policy. Unknown cluster origin stays `unknown`; a caller-supplied
Run is a declaration until independently authenticated. Site/tenant/local/unknown role and environment facts retain
explicit `declared` or `cluster-api` evidence levels and before/after phases.

Prepare a canonical array of `FileInput` records with `name`, `role` and independently reviewed
`expectedDigest`. Names are relative to the selected public root. Admit only reviewed public data;
original private logs and kubeconfig remain private. The emitter does not redact files or assess
whether their contents are safe to publish. Initial supported result format is bounded JUnit XML.
Its tokenizer uses Go's standard library; result projection preserves failure/error/skip semantics,
rejects ambiguous or contradictory counts and retains original bytes.

```sh
nvkr evidence emit --root public-results --description observation-description.json \
  --files selected-files.json --out observation-bundle
nvkr evidence verify-local --root observation-bundle --expected independent-expectations.json
```

Emission derives the inventory and summary and atomically creates a new bundle directory.
Existing output is never overwritten. `observation.json` covers members beneath `files/`.
The independent `Expected` document selects observation digest, input identities, tested subjects
and complete suite policy. It must be supplied independently of an untrusted locator or bundle.
Unsigned verification reports self-consistency and recorded case outcomes only, even when cases pass.

Explicit failed, canceled, infrastructure-error, unknown and not-run observations remain visible.
A completed empty suite, skipped required case, failed result or unsuccessful cleanup cannot pass.
No-result observations preserve zero cases without inventing a passing test.

## Publish and consume

Stock signing and authentication require Linux, an independently pinned native static cosign
executable, exact certificate identity and explicit issuer. Tool and capability admission precede
artifact or registry work. Production uses normal keyless/transparency verification; the local-key
interoperability tests do not supply a production public-key fallback.

```sh
nvkr evidence publish --root observation-bundle --repository registry.example/evidence/recipe \
  --cosign /approved/cosign --cosign-digest "$COSIGN_DIGEST" \
  --identity "$TEST_IDENTITY" --issuer "$OIDC_ISSUER"
nvkr evidence verify --locator reviewed-locator.json --expected independent-expectations.json \
  --cosign /approved/cosign --cosign-digest "$COSIGN_DIGEST" \
  --identity "$TEST_IDENTITY" --issuer "$OIDC_ISSUER" --anonymous --json
```

These are operator command templates; the repository, identity, issuer and destinations are explicitly
selected policy inputs. No public destination is configured by this feature. Publication uploads exact
content, attaches the fixed observation predicate and independently verifies the served content before
reporting success. Partial effects retain a locator and `successful: false`; they confer no authority.
Publication can record a failed test without issuing passing qualification.

The locator contains schema, pinned OCI reference and semantic observation digest. The OCI manifest
transport digest, canonical observation identity and signature bytes have distinct roles. Verification
by locator or `--reference repository@sha256:...` authenticates signer, issuer and statement subject,
checks the exact schema/inventory and every file, and recomputes results. It is passive and needs neither
the original cluster nor producer checkout. Credentials are host-scoped and anonymous OCI consumption
is explicit. Plain HTTP is restricted to explicitly selected loopback fixtures.

## Qualification and external acceptance

Observation signing is separate from build signing and qualification. Existing qualification v1 retains
its strict predicate and required-suite capability rejection; this first slice cannot satisfy that gate.
Kubernetes conformance execution, authenticated suite receipts and verified reporting belong to the next
two stacked changes. They must preserve complete artifact membership and the separate M3-B smoke gate.

The stack starts from PR143's `feat/m3-c-qualification-evidence` branch and preserves its owner's branch.
The separately owned NVKE nightly PR145 can later consume this interface. The human requested scoped
implementation and mergeable PRs; production evidence publication, cluster/hardware runs, TestGrid service
writes and tracker changes have no named authorization or destination in this task.

Local contract coverage includes bounded intake, path and member rejection, mutation, cancellation,
receipt closure, unsigned versus authenticated reporting, independent subject/policy checks and partial
attachment. A real Go fixture suite plus cosign 3.1.3 local-key loopback registry round trip exercises
original result publication and independent consumption. Linux signing race checks cover held execution.
These checks do not prove hosted OIDC acceptance, actual NVKE or Kubernetes test coverage, anonymous
public access, external-contributor acceptance or TestGrid ingestion. Those require separately recorded
exact runs, selected targets and authoritative read-back.
