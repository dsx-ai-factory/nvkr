# Navigate release artifacts and evidence

`nkdctl release browse` reads the bounded metadata and evidence of an exact release publication root and
shows its declared artifacts and memberships. It validates the complete metadata closure before displaying
any output, without downloading or reading artifact payload layers. It can read a registry or an existing
local OCI layout, and runs on macOS as well as Linux. Metadata inspection does not verify payload bytes,
signatures or consumer policy; use `release verify` for authenticated consumption.

## Start with the publication root

Use the release index reference emitted by publication, including its `@sha256:…` digest. Supply the full
registry and repository name. A tag, URL scheme, implicit registry, or an individual output manifest is not a
release publication root.

```sh
bin/nkdctl release browse --reference "$RELEASE_REF"
```

The default text view shows the exact root and content identities, the recorded Definition and Environment
identities, and each named output's type, derived payload reference, Build Digest, components and evidence.
Executable, native-image and disk platforms come from their checked config metadata. They describe the
declared artifact platform, without observing payload bytes. Platform is omitted where the metadata provides
no platform declaration; it is never inferred from an output name.

The displayed qualification is the publication's declared value. Every format labels the result as metadata
inspection and states that payloads, signatures and policy are unverified. Successful browsing does not
establish an accepted Release, independent reproduction, or operational qualification.

Registry access uses the existing local credential keychain by default. Add `--anonymous` for a public
registry. `--plain-http` explicitly enables HTTP for a local test registry. Inspection has a ten-minute default
deadline; `--timeout 20m` changes it. A caller's earlier cancellation still wins. Registry downloads use a
private system temporary directory that is removed on success, failure, or cancellation. By default, browsing
does not write to the current working directory. With `--workspace`, only the private inspection child is removed; the
caller-owned workspace remains. Registry browsing fetches the release index, metadata manifests and configs,
and bounded evidence documents from their derived sibling repositories. It checks exact metadata digests,
sizes, schemas and memberships. Artifact payload layers, including container layers, executable archives and
disk images, are not downloaded.

## Focus on one output

```sh
bin/nkdctl release browse --reference "$RELEASE_REF" --output nkectl-linux-arm64
```

Selection is an exact output name. Unknown names fail with a coded usage error. Selection changes the display
after all release metadata and evidence memberships have been checked; it cannot hide invalid sibling
metadata or missing evidence. Payload integrity remains outside browsing's scope.
Shared evidence keeps its complete list of output memberships in text and JSON even when only one output is
shown. Evidence references identify the exact manifest under the derived owning output repository.

## Export readable data or a diagram

```sh
bin/nkdctl release browse --reference "$RELEASE_REF" --format json > release-view.json
bin/nkdctl release browse --reference "$RELEASE_REF" --format mermaid > release-graph.mmd
```

JSON retains full artifact, manifest, blob, evidence-document and subject identities. It includes the total
output count and optional selected output, making a filtered view explicit. `referenceScope` labels locators as
derived from the supplied root reference, without claiming registry availability. Empty component build-subject
collections are arrays. Nonprinting Unicode, including bidirectional controls, is escaped for terminal display;
decoding the JSON preserves the exact string values. This indented JSON is presentation, not a canonical
identity document or a verification receipt.

The Mermaid diagram shows release membership, output components, and evidence-to-output and evidence-to-subject
relationships. It abbreviates digest labels for readability; use text or JSON for exact locators. This is the
published artifact/evidence graph. It does not reconstruct build ordering or claim relationships absent from
the publication metadata. Registry-provided labels are escaped before rendering.

## Inspect a local layout

```sh
bin/nkdctl release browse --reference "$RELEASE_REF" --layout /path/to/release-layout
```

The layout may contain the complete publication or only its metadata and evidence; browsing does not open
artifact payload layers in either case. The root digest must match the local index exactly. The repository namespace in the reference determines the
descriptive sibling references; a local view neither contacts that host nor proves those repositories contain
the artifacts. Text explicitly labels artifact/evidence locators as derived, and the detached description
makes no registry-availability claim in either acquisition mode. Registry and workspace flags cannot be combined
with `--layout`. The existing `release inspect --reference … --layout …`
continues to return its compact artifact array.

## Apply a verification policy separately

For authenticated consumption, follow [the lifecycle verification guide](lifecycle.md#run-and-verify) and pass
the same pinned reference to `release verify` with the consumer's accepted identity, issuer and evidence scope.
Browsing invokes neither cosign nor a signer, and does not treat a raw preparation SBOM as a signed attestation.
The existing Linux and capability requirements for policy verification still apply.

## Implementation and validation scope

Delivery owns the bounded metadata reader and the output/evidence projection. Lifecycle admits the reference
and options, bounds the operation, reads or pulls the metadata, obtains its detached description and removes
temporary resources. Presentation filters and formats only that description. No new release identity or
publication format is introduced. Explicit release verification retains its full payload and policy checks.

Contract tests cover anonymous and authenticated loopback registries, identical local/remote descriptions,
exact sibling references and evidence subjects, config-declared platforms, zero artifact payload-layer
downloads including disk images, failed admission before filesystem/network work, temporary-workspace cleanup
in a read-only current directory, rejection of evidence without output membership, empty Helm build-subject
arrays, filtered shared membership, Unicode-safe JSON round trips and CLI option admission. Those fixtures
establish contracts, not GHCR, signing-service or production acceptance. Current support and external evidence remain recorded in
[reconciliation](adr/reconciliation.md).

The [recorded loopback replay](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-03-release-navigation.md) exercised the earlier full-closure CLI
against a Docker registry carrying actual AMD64/ARM64 NKE CLI bundles and their dependency SBOMs. It is
unsigned diagnostic evidence, separate from signed release consumption and the current metadata-only path.
The [historical v3 metadata browsing validation record](https://github.com/dsx-ai-factory/nke-recipes/blob/e8bc47af0bc4c4017cf3dad1f13b03b1468f3643/docs/evidence/2026-10-06-release-browse-metadata.md) records
the scope and outcome of browsing the release published by Run `37533806963`. Current readers require
publication-root v4 and reject that earlier v3 release; the dated record does not establish current-schema
external acceptance.
