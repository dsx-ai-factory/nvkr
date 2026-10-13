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

Publication and verification each have a three-minute operation deadline, including content
transfer, signature attachment and read-back. A shorter caller deadline takes precedence. Large
bundles over slow connections may time out after partial immutable effects; inspect the returned
locator and unsuccessful status before retrying.

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

## Run Kubernetes conformance

The first concrete producer admits the declared upstream Hydrophone revision
`5fe47daaff5fe025215cb257118dc2a0e643e1d6`. It invokes an existing cluster and
retains upstream JUnit. It does not create the cluster or implement an alternative test framework.
A local kind cluster is a useful Kubernetes test environment; its role is `local`, and its results
cannot establish NVKE site/tenant coverage. AI validation and simulated GPU producers are later work.

Supply canonical `conformance.FileConfiguration` with schema
`nvkr.dev/conformance-invocation/v1`, exact tool/kubeconfig byte pins, one private context,
a CNI pod selector, digest-addressed upstream conformance and busybox images, exact stable image
version, explicit timeout seconds and an independent observation description. The upstream revision,
image digest and full `\[Conformance\]` focus must match its suite policy. Required cases are independently
selected from the upstream full conformance selection; never manufacture coverage from observed passes.
The declared image version must match the server at major.minor. Executable digests authenticate held
bytes, but do not prove those bytes were built from the declared Hydrophone revision; the declared version
is not independently joined to the selected image digest. Tool pins, declared revision/version and sidecar
image digest are recorded as declared public facts. Kubernetes API observations supply version and node/runtime/CNI
facts before and after; role, product binding and time remain declarations. No authenticated installed-byte
binding is inferred from container image status. Unknown source origin stays unknown.

```sh
nvkr evidence conformance --configuration private-conformance-invocation.json
```

Stock execution requires Linux with independently pinned native static Hydrophone/kubectl binaries.
The initial execution lane requires both `role=local` and `disposableCluster=true`. This acknowledgement
asserts operator ownership of a disposable test environment; it does not authenticate physical locality.
Site/tenant execution fails before work until
upstream normal cleanup can preserve ownership safely. Read-only observation and passive signed-suite
ingestion remain distinct.
Private invocation configuration and kubeconfig source files must have no group/other permission bits
(use `0600`, or `0400` for a read-only source). The kubeconfig must contain one TLS context with embedded
token or certificate/key; external
credential commands, ambient files and authentication-provider plugins are rejected. This narrow profile
can require preparing a self-contained test-only credential file. The producer receives no signing OIDC
credential. Its exact environment contains only private HOME/TMPDIR, empty PATH, a held KUBECONFIG
and private KUBECACHEDIR; discovery cache never uses the caller's directory. Timeouts bound every command,
suite, startup and cleanup. Only the uniquely owned test namespace and
matching RBAC resources are eligible for explicit UID-precondition cleanup; terminal absence is checked.
Hydrophone's normal automatic cleanup is name-based and upstream file downloads have no hard disk quota.
These remain operational limitations; the initial supported producer uses a disposable local test
environment and bounded outer runtime.

Build Hydrophone from the reviewed commit with `CGO_ENABLED=0 GOOS=linux GOARCH=<native architecture>
go build -trimpath -o <private binary> .`; a default dynamic build is rejected. Independently verify the
checkout and pin the resulting exact binary bytes. This is an operator build recipe, not a certified
source-to-binary receipt.

Timeout fields are integer seconds: `runTimeoutSeconds` is 1–43200, `commandTimeoutSeconds` is 1–300,
and `cleanupTimeoutSeconds` is 1–600. `startupTimeoutSeconds` is separate from API request timeout;
zero selects the pinned upstream five-minute default, and explicit values are 1–43200. The overall run
deadline still bounds startup. For cold image pulls, select an appropriate startup budget independently.
The description supplies initial `execution=not-run`, `cleanup=unknown`, empty `files` and zero `summary`;
actual execution replaces these placeholders and captures before/after facts. Supply real independent
input/subject identities rather than inventing release authority for a kind cluster.

This redacted kind configuration recipe uses the owning types and canonical codec. Replace all private
paths, context and pins; `independent-observation.json` is the separately reviewed observation description
with its original required-case list. Paths and credentials are never copied into the public facts.

```go
raw, err := os.ReadFile("independent-observation.json")
if err != nil { return err }
var observation testobservation.Observation
if err := canonical.UnmarshalExact(raw, &observation); err != nil { return err }
configuration := conformance.FileConfiguration{
    SchemaVersion: conformance.ConfigurationSchema,
    DisposableCluster: true,
    Hydrophone: conformance.Tool{Path: "/private/hydrophone", Digest: hydrophoneDigest},
    Kubectl: conformance.Tool{Path: "/private/kubectl", Digest: kubectlDigest},
    Kubeconfig: conformance.Tool{Path: "/private/kind-kubeconfig", Digest: kubeconfigDigest},
    Context: "kind-disposable-example", CNISelector: "app=kindnet",
    Image: "registry.k8s.io/conformance@" + conformanceImageDigest,
    ImageVersion: "1.37.0", // example only; independently select the tested version
    BusyboxImage: "registry.k8s.io/e2e-test-images/busybox@" + busyboxDigest,
    Observation: observation,
    RunTimeoutSeconds: 14400, CommandTimeoutSeconds: 120,
    StartupTimeoutSeconds: 900, CleanupTimeoutSeconds: 120,
}
encoded, err := canonical.Marshal(configuration)
if err != nil { return err }
return os.WriteFile("private-conformance-invocation.json", encoded, 0o600)
```

Derive required cases **before running** from the selected exact upstream image. For the inspected
Kubernetes v1.37.0 profile, the independent command is:

```sh
docker run --rm --network none --entrypoint /usr/local/bin/e2e.test \
  "registry.k8s.io/conformance@$CONFORMANCE_IMAGE_DIGEST" --list-tests
```

That profile emits a heading followed by source-file/line-prefixed full test names. Retain full names
containing the literal `[Conformance]` without trimming or reconstructing their text, reject duplicate
names, and review the complete selected list. The corresponding upstream JUnit case uses suite and class
`Kubernetes e2e suite` and name `[It] ` followed by the full text. This mapping is version-specific;
verify it for another upstream profile rather than deriving requirements from that run's passing results.
The inspected v1.37.0 image enumerated 462 selected cases out of 3206; those numbers are descriptive and
are not hard-coded acceptance defaults. Keep the list command, image digest and reviewed selection with
the private invocation record.

The command returns the private workspace and execution/cleanup diagnostics, including on failure.
Private `hydrophone.stdout.log` (16 MiB maximum) and `hydrophone.stderr.log` (1 MiB maximum), each
mode `0600`, retain harness diagnostics; excess diagnostic output is truncated
without canceling the test. API and original-result byte budgets remain fail-closed. Cancellation retains
its coded cause while bounded cleanup separately waits for terminal absence.
`observation-description.json` and `review-junit.xml` there are unsigned candidates. Review the original
JUnit and public facts, explicitly select public files and raw pins, then use the separate `evidence emit`
command. Never publish the entire private workspace. `conformance.Result.Emit` provides the same narrow
SDK projection after explicit reviewed JUnit digest admission. Cancel/infra/no-result records remain
nonpassing and never fabricate a successful case.

## Qualify authenticated required suites

`run qualify-tests` selects the required-suite v3 extension; `run qualify` keeps the native v2
contract. The extension preserves mandatory authenticated native smoke and the complete native policy.

```sh
nvkr run qualify-tests --policy independently-selected-native-policy.yaml \
  --suite-config independently-selected-suites.json --workspace private-qualification
```

The bounded canonical `qualification.SuiteConfigurationV3` uses
`nvkr.dev/qualification-suite-configuration/v3`. Its `PolicyV3` uses
`nvkr.dev/qualification-policy/v2`, binds the complete native policy digest and selects required suites.
Native `nvkr.dev/qualification-policy/v1` remains unchanged; its own `requiredSuites` must stay empty.
The extension cannot upgrade a suite declared outside scope by that native policy.
Generate JSON with the owning canonical codec. Nested transport/credential options retain their owning
serialization names. The configuration must be a regular private file with no group/other permissions
(use mode `0600`); admission checks the held descriptor. Keep credentials out of public summaries.

Each requirement selects exact observation digest, inputs, tested subjects and suite policy, literal signer
and issuer, and a site/tenant role. The qualifier authenticates its triggering hosted Run and invocation,
complete artifact membership and native workers before joining independently verified suite receipts.
Each suite must cover every exact named output and every required native child, including its native
manifest and Build identity. Closed, canceled, wrong-attempt or incomplete receipts cannot sign.
Local kind observations cannot satisfy site/tenant policy. Required-case skips always block; skips outside
required selection remain visible. Authenticated installed-deployment binding is unsupported.

The generated qualifier/worker pair still emits native v2. Generated v3 activation is deferred;
changing its final command requires an explicit generator/policy extension and regeneration. The v3
API and CLI contracts are tested, but no hosted v3 qualification or production acceptance is recorded.
Raw-file publishing refuses before I/O because it cannot authenticate native smoke completion.

## Discover and report verified observations

`evidence report` consumes a canonical list of `testreporting.Input`: a digest-pinned locator,
independent exact expectations, and explicitly reviewed `Admission` for each source. Admission selects
literal signer/issuer, first-party or community tier, exact registry namespace/source path and Definition.
There is no default allowlist. Every locator is authenticated before deduplication; a signature or cached
index cannot expand namespace/subject policy. The generated discovery index retains historical attempts,
failures, tested coverage, signer tier, exact references and reproduction inputs. It is generated metadata,
not another authored lifecycle object or a claim about current health.

```sh
nvkr evidence report --inputs reviewed-evidence-inputs.json \
  --cosign /approved/cosign --cosign-digest "$COSIGN_DIGEST" \
  --anonymous --format markdown --out verified-results.md
```

Use `--format json` for a generated discovery index. All requested projections, including TestGrid
membership and timestamp gates, are admitted before any output commit. Files and directories are immutable;
receipt lifetime is rechecked before and after the commit. `reportCommitted` and `testGridCommitted` identify
actual commits, and each output path is reported only after its rename commits. A late failure retains that
explicit partial state and performs no rollback. Exports use private directories (`0700`) and files (`0600`).

At most 128 inputs, 256 MiB of cumulative authenticated observation/covered-file bytes and 512 held files
including control documents are retained, in addition to per-bundle limits. One independently bounded
candidate inventory may be held temporarily before an aggregate gate rejects and closes it. Every input
is authenticated before deduplication, including duplicates. Conflicting tier, Definition or signer admission
for one namespace/source path fails before verification; first-party sources cannot use `community/` paths.
With explicit credentials every input must use the single `NVKR_REGISTRY` host. Use `--anonymous` or separate
report invocations for different registry hosts.

Community paths use the independently selected Definition and an authenticated signer grouping digest:
`community/<Definition digest hex>/signer-<grouping digest hex>`. The grouping digest uses the registered
`nvkr.community-signer-label` canonical family over exact identity and issuer. It is deterministic and
unsalted, so candidate identities can be checked offline. It supplies no confidentiality or authorization;
raw signer identity and issuer remain disclosed in the index, Markdown and TestGrid evidence sidecar.
The cross-signer collision guard is defense in depth. Reporting grants no qualifier role.

The stock `testobservation.VerifyOCI` owner authenticates signatures and exact covered files; its bundle
and registry tests exercise tamper rejection. Reporting tests inject private receipt fixtures to exercise
source admission, aggregation, revocation, deduplication and export orchestration. These tests do not prove
public registry, contributor or TestGrid service acceptance.

### TestGrid local export and companion work

Add paired `--testgrid-target reviewed-testgrid-target.json --testgrid-out local-testgrid-export` to export
an explicitly selected observation/source as a local TestGrid directory. The canonical
`testreporting.TestGridDestination` selects bucket, the complete `groups/group/dashboard/tab/build` prefix,
observation digest, namespace and source path. No bucket, tab, credential or retention default is invented.
The exporter retains exact original JUnit, writes started metadata first, then evidence/results, and finished
metadata last. It binds deterministic JUnit transport labels to original filenames/raw pins in the evidence
sidecar. No-result failures contain explicit status without synthetic passing cases. Producer timestamps are
signed declared facts, not trusted time. TestGrid publication remains `published: false`.

The inspected Autotest publisher at `25bf36a133f5681ded8e5b341f87a2df80aca7eb`,
`framework/reportingservice/destinations/testgrid_destination.py:859-887`, writes started metadata, original
JUnit, then finished metadata. TestGrid inspected at `83d7f9234d3137d36cd2ff0004d938a0dbb537ee` pins upstream
module `v0.0.175`: `util/gcs/read.go:279-280` admits the JUnit basename pattern and
`pkg/updater/read.go:418-419` reads all matching files. Its updater manifest declares
`v20251223-v0.0.175-2-g0e84967e`. These are historical source/configuration observations; they prove neither
currently deployed image bytes nor actual ingestion.

Multiple JUnit files keep their original bytes under full filename-digest transport labels. TestGrid may
render those hashes as filename-derived contexts; `artifacts/evidence.json` maps them back to exact original
filenames, raw digests and lengths. Historical backfill visibility depends on the selected TestGroup's
retention and build ordering; retaining the original producer timestamp does not guarantee display.

The inspected config-gen at `tools/config-gen/main.go:644-648` projects only `release_stream`, `release_tag`,
`runtime_release` and `operations_release`. The [column-header example](../pkg/nvkr/testreporting/testdata/testgrid-column-header-example.json)
is a partial field fragment, with every key checked against emitted metadata; it specifies no target.
A selected TestGrid backend still needs a companion registered-group/column-header change to render exact
evidence links, tier and no-result status. This local export provides no service integration acceptance.
A future uploader requires an operator-selected bucket/tab/access/retention contract before a concrete deployable change
or uploader can be prepared. Service authorization, immutable object writes and authenticated API/UI read-back
remain unresolved. Public hosting and anonymous access also need a selected destination and read-back; local
index generation proves neither. No external service writes or production evidence publication occur here.
