# Navigate release artifacts and evidence

`nkdctl release browse` opens an exact release publication root and shows the artifacts and evidence it
contains. It checks the complete content closure before displaying any output. It can read a registry or an
existing local OCI layout, and runs on macOS as well as Linux. Inspection does not verify signatures or apply
a consumer policy; use `release verify` for that separate decision.

## Start with the publication root

Use the release index reference emitted by publication, including its `@sha256:…` digest. Supply the full
registry and repository name. A tag, URL scheme, implicit registry, or an individual output manifest is not a
release publication root.

```sh
bin/nkdctl release browse --reference "$RELEASE_REF"
```

The default text view shows the exact root and content identities, the recorded Definition and Environment
identities, and each named output's type, payload reference, Build Digest, components and evidence. Executable
and native-image platforms come from their checked payload configs. Platform is omitted for kinds where this
reader has no payload platform observation; it is never inferred from an output name.

The displayed qualification is the publication's declared value. Every format states that inspection checks
integrity only and does not verify signatures or policy. Successful browsing does not establish an accepted
Release, independent reproduction, or operational qualification.

Registry access uses the existing local credential keychain by default. Add `--anonymous` for a public
registry. `--plain-http` explicitly enables HTTP for a local test registry. Inspection has a ten-minute default
deadline; `--timeout 20m` changes it. A caller's earlier cancellation still wins. Registry downloads use a
temporary directory under `.nkd/inspected-releases`, or under `--workspace`, and are removed on success or
failure. Opening a large image release downloads its full payload and evidence closure.

## Focus on one output

```sh
bin/nkdctl release browse --reference "$RELEASE_REF" --output nkectl-linux-arm64
```

Selection is an exact output name. Unknown names fail with a coded usage error. Selection changes the display
after the whole publication has been checked; it cannot hide invalid sibling artifacts or missing evidence.
Shared evidence keeps its complete list of output memberships in text and JSON even when only one output is
shown. Evidence references identify the exact manifest in its owning output repository.

## Export readable data or a diagram

```sh
bin/nkdctl release browse --reference "$RELEASE_REF" --format json > release-view.json
bin/nkdctl release browse --reference "$RELEASE_REF" --format mermaid > release-graph.mmd
```

JSON retains full artifact, manifest, blob, evidence-document and subject identities. It includes the total
output count and optional selected output, making a filtered view explicit. This indented JSON is presentation,
not a canonical identity document or a verification receipt.

The Mermaid diagram shows release membership, output components, and evidence-to-output and evidence-to-subject
relationships. It abbreviates digest labels for readability; use text or JSON for exact locators. This is the
published artifact/evidence graph. It does not reconstruct build ordering or claim relationships absent from
the publication metadata. Registry-provided labels are escaped before rendering.

## Inspect a local layout

```sh
bin/nkdctl release browse --reference "$RELEASE_REF" --layout /path/to/release-layout
```

The root digest must match the local index exactly. The repository namespace in the reference determines the
descriptive sibling references; a local view does not prove those repositories contain the artifacts. Registry
and workspace flags cannot be combined with `--layout`. The existing `release inspect --reference … --layout …`
continues to return its compact artifact array.

## Apply a verification policy separately

For authenticated consumption, follow [the lifecycle verification guide](lifecycle.md#run-and-verify) and pass
the same pinned reference to `release verify` with the consumer's accepted identity, issuer and evidence scope.
Browsing invokes neither cosign nor a signer, and does not treat a raw preparation SBOM as a signed attestation.
The existing Linux and capability requirements for policy verification still apply.

## Implementation and validation scope

Delivery owns the held closure and the output/evidence projection. Lifecycle admits the reference and options,
bounds the operation, opens or pulls the publication, obtains its detached description and closes the held
resources. Presentation filters and formats only that description. No new release identity or publication
format is introduced.

Contract tests cover anonymous and authenticated loopback registries, identical local/remote descriptions,
exact sibling references and evidence subjects, payload-derived executable platform, defensive copies, closed
owners, failed admission before filesystem/network work, cancellation cleanup, filtered shared membership,
label escaping and CLI option admission. Those fixtures establish contracts, not GHCR, signing-service or
production acceptance. Current support and external evidence remain recorded in
[reconciliation](adr/reconciliation.md).

The [recorded loopback replay](evidence/2026-10-03-release-navigation.md) additionally exercised the built CLI
against a Docker registry carrying actual AMD64/ARM64 NKE CLI bundles and their dependency SBOMs. It is
unsigned diagnostic evidence, separate from signed release consumption.
