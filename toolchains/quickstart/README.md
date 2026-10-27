# Quickstart Go SDK checksums

The quickstart can install the exact Go SDK from `.go-version` inside its own checkout after user consent.
These checksum files use the standard `sha256sum --check` format.
The digests were read from the [official Go download metadata](https://go.dev/dl/?mode=json&include=all)
for Go 1.27.3; update all four files when changing that version.

The pinned utility container supplies curl and SHA-256 verification, without a Docker socket mount.
The selected archive targets the host OS and CPU. After verification, the host extracts the SDK
and uses it only in the child launcher's PATH. No system Go installation or shell profile is changed.
The SDK archive, failed download directories and build outputs remain under `.nvkr/`.
