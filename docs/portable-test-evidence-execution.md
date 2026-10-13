# Portable test evidence

Portable observations bind exact input and tested output identities, independently selected suite
policy and required cases, invocation, public environment facts, execution and cleanup outcomes,
and an inventory of original approved result files. Authentication proves the selected attester's
recorded claim. It does not establish physical hardware, installed deployment bytes or current health.
[Reconciliation](adr/reconciliation.md) owns current support and evidence.

## Emit and inspect locally

Prepare a canonical `testobservation.Observation` description with schema
`nvkr.dev/test-observation/v1`, an empty `files: []` inventory and zero `summary`. Required-case lists and inventories
use arrays, including `[]` for an empty list; wire `null` is rejected. Each input, subject,
suite revision, image digest, selection, required-case list and policy digest comes from its owning
source or explicitly reviewed test policy. Unknown cluster origin stays `unknown`; a caller-supplied
Run is a declaration until independently authenticated. Site/tenant/local/unknown role and environment facts retain
explicit `declared` or `cluster-api` evidence levels and before/after phases.

Prepare a canonical array of `FileInput` records with `name`, `role` and independently reviewed
`expectedDigest`. Names are relative to the selected public root. Admit only reviewed public data;
original private logs and kubeconfig remain private. The emitter does not redact files or assess
whether their contents are safe to publish. Initial supported result format is bounded JUnit XML.
Its tokenizer uses Go's standard library; result projection preserves failure/error/skip semantics,
rejects ambiguous or contradictory counts and retains original bytes. Ginkgo suite setup/cleanup
entries contribute to raw counts but never satisfy required test-case coverage. Ordinary duplicate
test identities remain errors; bounded failure diagnostics may exceed the identity-field limit.

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
The JSON field `recordedRequiredCasesPassed` describes historical outcomes;
`signerAuthenticated` is a separate claim and unsigned checks leave it false.

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
  --identity "$TEST_IDENTITY" --issuer "$OIDC_ISSUER" --anonymous > publication.json
jq --sort-keys --compact-output --join-output .locator publication.json > reviewed-locator.json
nvkr evidence verify --locator reviewed-locator.json --expected independent-expectations.json \
  --cosign /approved/cosign --cosign-digest "$COSIGN_DIGEST" \
  --identity "$TEST_IDENTITY" --issuer "$OIDC_ISSUER" --anonymous --json
```

These are operator command templates; the repository, identity, issuer and destinations are explicitly
selected policy inputs. No public destination is configured by this feature. Observation transport requires
explicit `--anonymous` or complete host-scoped credentials through `NVKR_REGISTRY`, `NVKR_REGISTRY_USER`
and `NVKR_REGISTRY_PASSWORD`. Anonymous mode ignores these environment credentials. Ambient Docker
keychains and credential helpers are never consulted, because their resolution does not honor request
cancellation. Cosign creates and verifies a standard Sigstore bundle without registry access or registry credentials.
The observation owner uploads and retrieves its standard OCI referrer through guarded ORAS transport.
Publication uploads exact
content, attaches the fixed observation predicate and independently verifies the served content before
reporting success. Partial effects retain a locator and `successful: false`; they confer no authority.
Publication can record a failed test without issuing passing qualification.

Publication JSON is a result wrapper containing `locator` and `successful`; it is not itself
a locator. Extract its locator as shown above. The locator consists of simple ASCII schema,
digest and registry-coordinate strings, so sorted compact JSON without a trailing newline
preserves its canonical encoding. A partial result has `successful: false`; its coordinates
allow inspection of partial effects and do not imply successful publication.

The locator contains schema, pinned OCI reference and semantic observation digest. The OCI manifest
transport digest, canonical observation identity and signature bytes have distinct roles. Verification
by locator or `--reference repository@sha256:...` authenticates signer, issuer and statement subject,
checks the exact schema/inventory and every file, and recomputes results. It is passive and needs neither
the original cluster nor producer checkout. Registry manifests, uploads and token realms remain restricted to the admitted scheme and host;
registries requiring a separate authentication host fail closed. Exact covered blob GETs may follow
HTTPS storage redirects after authorization and cookie headers are removed. Size and raw digest
checks still apply to the returned bytes. Anonymous OCI consumption is explicit. Plain HTTP
is restricted to selected loopback fixtures.

## Qualification and external acceptance

Observation signing is separate from build signing and qualification. Native qualification v2
requires authenticated native smoke and rejects required external suites through its own policy.
Observation receipts alone cannot satisfy that gate. Kubernetes conformance execution, authenticated
required-suite integration and verified reporting are separate capabilities; reconciliation records
their support. They must preserve complete artifact membership and native smoke authorization.

Local contract coverage includes bounded intake, path and member rejection, mutation, cancellation,
receipt closure, unsigned versus authenticated reporting, independent subject/policy checks and partial
attachment. A real Go fixture suite plus cosign 3.1.3 local-key loopback registry round trip exercises
original result publication and independent consumption. Linux signing race checks cover held execution.
These checks do not prove hosted OIDC acceptance, actual NVKE or Kubernetes test coverage, anonymous
public access, external-contributor acceptance or TestGrid ingestion. Those require separately recorded
exact runs, selected targets and authoritative read-back.
