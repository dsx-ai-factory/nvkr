# Native ARM64 AICR CLI

The `aicr-aicr` source in the parent catalog pins the Linux ARM64 AICR CLI release asset. The tarball's `aicr-attestation.sigstore.json`
is a pinned companion, delivered beside `aicr` in the same executable output. AICR looks for that file
next to the running executable and copies it into attested bundles. Without it, `aicr bundle --attest`
cannot chain binary provenance into a bundle.

Update the version through the CLI:

```sh
bin/nvkr catalog pin --dir inputs/catalogs/aicr --tag v1.1.2 aicr-aicr
```

The lock binds the source revision, release archive, executable and companion bytes. `evidence.none`
records that AICR's signed attestation and SBOM are not verified yet. Acquisition does not establish a
signed or qualified release.
