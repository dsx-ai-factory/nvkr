# Statement coverage improvement

This historical coverage record predates the Recipe removal. The figures and package descriptions below
apply to the earlier model, including the retired Recipe, criteria, and composition packages; they are not
measurements of the current implementation.

## Goal and measurement

Raise repository-wide statement coverage above 80% with meaningful contract and integration tests.
The gate remains `make check`, including `go test -race -covermode=atomic -coverprofile=coverage.out ./...`.
No packages, production statements or failure paths are excluded to improve the number.

Initial macOS coverage was 64.2%: 16,667 of 25,955 statements. A separate cross-package-instrumented diagnostic
reported 68.4% after the first CLI tests; that diagnostic is not substituted for the existing gate metric.
Linux is measured separately because descriptor-execution tests intentionally cannot execute on macOS.

## Current verified increment

The full native gate passes at 71.8%. CLI coverage is 66.4%; factory coverage is 75.5%; delivery coverage is 68.5%;
criteria coverage is 100.0%; IR coverage is 83.8%.
The isolated Linux/arm64 full uncached race suite passes at 74.0%, including every test batch listed below.
The Linux pipeline race suite passes. Native pipeline coverage is 67.8%;
Linux pipeline coverage is 68.0%.
Native vulnerability coverage is now 92.3%.
Native transport coverage is 78.8%.
Native composition coverage is 89.7%.
Native lifecycle coverage is 79.4%.
Native trust coverage is 83.6%.
Native status coverage is 79.1%.
Native catalog coverage is 80.6%.
Native client coverage is 64.2%.
Native cluster coverage is 84.2%.
Native release coverage is 78.1%.
Native assembly coverage is 61.7%; held codec execution is Linux-only.
The Linux run uses a snapshot of the working sources, the local module cache and no external network.

Added tests cover:

- Public Definition inspection/validation identity, Run plan text/JSON, generation/check drift, dry-run storage
  invariants and coded rejection of malformed inputs through the actual CLI application.
- Every hosted restore outcome, exact terminal-observation input bytes and membership, recipe coverage,
  path escapes, and production-predicate plan/identity substitution.
- Local Distribution registry round trips with anonymous and Basic authentication, recursive bundle recovery,
  immutable output destinations, malformed references, conflicting credentials, cancellation and failures.
- Executable and APT package observation from actual packaged bytes and producer records, missing or ambiguous
  recipe identity, substituted component membership, and canonical publication bindings. Publication tests
  require producer evidence to be declared as an input even when its file is present in the workspace.
- Local Git pin resolution with lightweight and annotated module-prefixed tags, exact commit/tree/epoch/Go
  version capture, missing refs, and a scripted Docker digest-parsing contract. The Docker fixture does not
  claim a real registry pull. Prebuilt targets render before artifact digests exist and reject bad templates.
- Loading and executing a real compiled factory plan's observation/package nodes; directory observation
  stability under relocation and sensitivity to bytes, paths and modes; rejection of symlink entries.
- Cluster publication member bindings and rejection of changed recipe, composition, role and output joins.
- Binary checksum-sidecar selection and download fallback against local HTTP servers, plus bounded reads,
  oversized/truncated responses, HTTP errors and cancellation without returning partial data.
- OCI image/index acquisition from a local registry, offline cache reuse, corrupt cache rejection, canceled
  staging cleanup, nested/shared descriptor closures, malformed descriptors and ambiguous JSON rejection.
- CLI prebuilt acquisition via a trusted local HTTPS fixture, explicit component and role selection, dry runs,
  and consistent recipe identity across canonical, execution-graph and display output formats.
- CLI source pin/bump writes and dry-run lock preservation through canonical URLs redirected to a local Git
  repository; per-architecture artifact pin updates and unchanged-pin detection through local HTTPS.
- Criteria domain ownership, category partitioning, reserved-axis handling, AICR tuple requirements and
  deterministic resolution defaults. IR rejection tests cover build fields, environments, patch pins and
  acquisition shapes with exact error codes.
- Publication read/extraction checks for detached exact payloads, malformed index bindings and typed model
  failures; binary layer rejection for missing, duplicate, undeclared, symlinked or mismatched members.
  APT tests cover both architectures, input-order independence, exact Acquire-By-Hash metadata, package
  substitution, duplicate rejection and multiline control descriptions.
- Local runner platform/environment and exact plan serialization, runtime isolation arguments and cleanup,
  capability admission, failed compilation stage cleanup, workspace ownership rejection and exact tool-cache
  reuse. Compiler/runtime scripts exercise process contracts, not real builds or container execution.
- Compiler cluster bindings to exact recipe identities and selected roles, emitted composition identity,
  package dependency ordering and cycle rejection, explicit AICR selection, and Helm bindings requiring the
  exact image package and recipe source archive. Invalid package representations and unsupported routes fail
  with the expected codes.
- Profile CLI canonical inspection for both architectures and authored inputs, exact runner/toolchain repin
  bytes and identity, preservation of the source generation, rejection of malformed pins and signed overwrite
  requests, and host preflight divergence reporting with enforced and diagnostic behavior.
- Per-artifact diagnostic SBOM files and their index bindings to the emitted SBOM digest and artifact hash;
  file-based bake-off comparisons for matching and changed artifacts, missing files and malformed records.
  Successful comparisons remain explicitly unqualified and do not become validation evidence.
- Trivy finding normalization and ordering, exact serialized report identity, and strict rejection of malformed
  Grype/Trivy shapes, incomplete findings, unknown severities, duplicate names and trailing JSON. These tests
  exercise the scanner adapter contract; they do not claim to execute a real scanner or vulnerability database.
- Filesystem object-store round trips, missing objects, writer failures and read cancellation; invalid OCI,
  package, object and referrer identities are rejected before creating destination state.
- Composition loader coverage for nested mixins, malformed YAML, strict fields, duplicate identities and
  provider read failures. Errors return no partial store; valid mixins preserve the complete component set.
- Lifecycle file preparation using the maintained NKE inputs, dry-run preservation of frozen requirements
  without reserving attempt storage, defensive description copies, and rejection of missing inputs or invalid
  attempt names without issuing a prepared capability.
- Local publication-layout inspection binds every projected object to its exact blob digest, rejects a
  mismatched reference or invalid object identity, and preserves the distinction from authenticated Release
  verification. Failed consumer materialization cleans staging and preserves an obstructing existing file.
- Disk-manifest validation and serialization reject malformed RAW descriptions, encrypted/masked identities,
  invalid block pins and ambiguous envelope lists. ISO serialization preserves the flat-stream identity.
- Tool archive acquisition against local HTTP: exact archive and executable identities, unrelated members,
  executable permissions, missing/duplicate/symlinked executables, bad gzip and HTTP failures. Rejected
  downloads create no output state.
- Compilation resolves catalogs relative to the authored Definition, supports absolute catalog transport paths
  and per-recipe overrides, preserves recipe identity across equivalent locations, and rejects missing overrides.
  Overlay files bind the exact predecessor and resulting catalog digests; missing files and wrong predecessor
  pins return no stack. Relative and absolute overlay paths apply the same component replacement.
- Collected artifacts preserve exact bytes and modes. Missing, symlinked, escaping and canceled sources are
  rejected before output creation; a failed destination rename removes its staging file.
- Source-closure receipts on temporary Git repositories bind exact commit/tree identities, run vendoring once,
  reject cached closure drift and unreceipted tracked/untracked/ignored files, and remain absent after vendor
  admission or execution failure. The vendor callback is a contract fixture, not an external dependency build.
- Execution-bundle drift checks distinguish stale recipe pins, hand-edited workflows and incomplete members;
  existing file state binds exact bytes and rejects symlinks.
- Lifecycle models reject ambiguous Release artifact order, invalid Environment publication/signing settings,
  unsafe tool paths and mismatched raw tool pins. Invalid Environments receive no canonical identity.
- Trust-document authentication rejects malformed, duplicate, trailing and incorrectly typed JSON even after
  successful verifier execution. Receipts retain schema, issuer, role, snapshot and proof-byte bindings from
  the verifier fixture; these tests do not claim live signing-service verification.
- Published container inspection rejoins the exact native manifest, index, platform and entrypoint. Missing
  selections, wrong object types, unsupported or mismatched platform bindings and corrupted nested content
  return no image identity. These tests use real local OCI layouts without registry publication or execution.
- Image-artifact pinning authenticates requested Linux manifests and parent membership through the pinned
  OCI library. Controlled TLS registry fixtures cover both native media families and architectures,
  complete built-in acquisition, observed results, cached closure tampering and absent membership.
  These fixtures do not establish live upstream acquisition, signing or production delivery.
- Checkpoint shape validation rejects invalid targets, predecessors, declaration sets and lifetimes before
  producing signing bytes or identity. Authenticated receipt access keeps blob integrity separate from chain
  identity, returns detached signer lists and binds the next high-water mark to the accepted target and authority.
- The action-bump command updates only selected pins, persists explicit track changes even at an unchanged
  commit, and preserves exact lock bytes for check-only, current-pin and failed API operations. API fixtures
  validate request selection and token propagation without contacting GitHub or modifying repository pins.
- Catalog composition preserves selection order, merges nested stamps, replaces commands and leaves source
  values unchanged after overrides. Invalid overrides and missing components return no partial selection.
  Declaration tests reject ambiguous recipe membership, incomplete Nix pins, mutable builder images and
  unsupported prebuilt download locations without claiming that those builders or downloads executed.
- Factory graph tests start with valid execution inputs and reach specific dataflow rejection boundaries:
  missing inputs, producer mismatch, undeclared artifacts, repeated edges, self-dependencies and cycles.
  Shared-producer inputs remain valid. Plan tests reject changed environment/recipe identities, incomplete
  source paths, missing recipe graphs and noncanonical selections before execution.
- Transport snapshot tests bind exact held bytes, reject changed/closed/missing sources and canceled copies,
  leave no snapshot pathname, and preserve an existing destination on collision. Stable digest rechecks
  return no identity when the held source or its metadata is unavailable or changed.
- Public SDK document loading preserves authored Environment identity and returns no partial model for
  malformed, missing, invalid or symlinked inputs. Invalid configuration and cancellation fail admission
  before loading or preparing lifecycle documents.
- Diagnostic publication commands join canonical Inventory and ReleaseSet identities, expose those bindings
  through JSON inspection and extract both documents byte-for-byte. Incomplete pairs and mismatched inventory
  references create no output directory. These local layouts remain explicitly unqualified.
- Disk-manifest CLI tests measure exact RAW/ISO streams, preserve logical and canonical manifest identities,
  and reject incompatible envelope formats before file access. Decoder selection rejects missing or ambiguous
  components and requires a pinned decoder before execution; these cases do not claim a successful decode.
- Cluster composition rejects invalid role, category, recipe-frame and AICR bundle bindings before issuing
  canonical bytes or an identity. Normalization sorts nested selections and remains byte- and identity-stable
  when repeated. These structural checks do not claim recipe authentication or runtime qualification.
- Promotion-policy tests reject malformed pins, invalid lifetimes, unknown artifact/evidence classes and
  duplicate or unknown waiver reasons before producing signing bytes. Canonicalization preserves authored
  collection order and returns detached nested waiver rules with stable normalized identity.
- Release authorization projections retain exact ReleaseSet, Inventory and publication identities and return
  detached clearance bytes and evidence memberships. Nil and zero receipts expose no bindings. The existing
  authorization fixture exercises module contracts, not live signing services.
- Delivery constructors reject incomplete publication bindings, duplicate objects, wrong byte pins and
  missing or symlinked bundle members without returning identities or leaving staging/output files behind.
- Factory publication execution pushes to an in-process registry, writes the exact digest-pinned reference,
  and recovers the same root on pull. Invalid cardinality, unsupported adapters and missing configured
  credentials return no output reference. This is local transport evidence, not hosted signing evidence.
- Factory AICR tests select bundle settings by recipe identity and reject missing activation, ambiguous
  generator inputs, invalid profiles and unavailable execution before output creation. Native admission
  coverage does not establish successful generator execution or qualified bundle production.

The CLI application constructor is separated from process signal/exit handling so tests exercise the actual
command registration. No public command behavior is intentionally changed.

The production-build-type test exposed a correctness bug: any source build had been classified as hermetic,
including networked BuildKit builds. The classifier now uses the wrapped build type whenever any build needs
network access or the wrapped builder. Earlier published bytes/signatures remain real, but their build-type
claim was too strong; the dated combined-run evidence records this limitation. The correction is local and
has not rewritten or republished the earlier signed attestation.

## Remaining work

Coverage is below the requested threshold. The largest remaining gaps are factory execution/lifecycle bindings,
pipeline execution/transport, CLI diagnostic and pinning paths, delivery/qualification, and evidence modules.
Continue with actual behavior and failure scenarios, using both package coverage and uncovered-statement counts
to select the next work. Run the full gate and Linux coverage again before claiming the target is achieved.

Unrelated edits to the milestone/epic specifications, Jira drafts and site-controller example remain untouched.
