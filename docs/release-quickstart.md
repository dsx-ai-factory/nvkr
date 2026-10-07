# Browse and verify a release

Explore the outputs and evidence in an existing release, then apply your consumer policy separately.
Start at the repository root with `bin/nkdctl` built using the [quickstart](quickstart.md). Browsing works
on macOS and Linux; verification below requires Linux.

## Find an output

Set `RELEASE_REF` to the exact publication-root reference emitted by a completed publication:
the full registry/repository followed by `@sha256:` and its digest. A tag, URL, or individual output
manifest is not a release root. Obtain this reference from the release publisher.

```sh
: "${RELEASE_REF:?Set RELEASE_REF to an exact publication-root reference}"
bin/nkdctl release browse --reference "$RELEASE_REF"
```

The listing shows named outputs, artifact types, components, Build Digests, and evidence. Registry access
uses your existing credentials; add `--anonymous` to each command for a public registry.

Set `OUTPUT_NAME` to an exact name from that listing. Keep using the same terminal:

```sh
: "${OUTPUT_NAME:?Set OUTPUT_NAME to an exact name from the listing}"
NKD_RELEASE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nkd-release.XXXXXX")
bin/nkdctl release browse --reference "$RELEASE_REF" --output "$OUTPUT_NAME"
bin/nkdctl release browse --reference "$RELEASE_REF" --output "$OUTPUT_NAME" \
  --format json > "$NKD_RELEASE_DIR/output.json"
bin/nkdctl release browse --reference "$RELEASE_REF" --output "$OUTPUT_NAME" \
  --format mermaid > "$NKD_RELEASE_DIR/output.mmd"
```

JSON preserves complete identifiers. Open the Mermaid export in a compatible viewer to follow output,
component, and evidence relationships. This is release membership, not build ordering.

Browsing validates all release metadata before filtering, without downloading artifact payload layers,
including OS disk images. It does **not** verify payload bytes, signatures, or your acceptance policy.

## Verify against an accepted policy

On Linux, use a pinned, static Linux cosign executable and independently accepted signer identity pattern
and OpenID Connect issuer. Set the four variables below from your trusted tool configuration and consumer
policy; do not derive accepted identities from an untrusted candidate merely to make it pass.

```sh
: "${ACCEPTED_BUILD_IDENTITY_REGEXP:?Set the accepted signer identity pattern}"
: "${ACCEPTED_OIDC_ISSUER:?Set the accepted OpenID Connect issuer}"
: "${COSIGN_PATH:?Set the path to the static Linux cosign executable}"
: "${COSIGN_DIGEST:?Set the independently trusted cosign SHA-256 digest}"
bin/nkdctl release verify --reference "$RELEASE_REF" \
  --lane pipeline-attested --scope artifact-publication \
  --identity "$ACCEPTED_BUILD_IDENTITY_REGEXP" --issuer "$ACCEPTED_OIDC_ISSUER" \
  --cosign "$COSIGN_PATH" --cosign-digest "$COSIGN_DIGEST"
```

This scope checks artifact bytes, signatures, and production attestations under that policy. It does not
provide qualified acceptance or the capability-gated full `release` scope.

See [release navigation](release-navigation.md) for local layouts and inspection details, and the
[verification lifecycle](lifecycle.md#run-and-verify) for policy and capability requirements.
