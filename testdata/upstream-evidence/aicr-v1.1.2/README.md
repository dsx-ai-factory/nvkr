# AICR v1.1.2 upstream evidence fixtures

Unmodified release evidence from [NVIDIA/aicr v1.1.2](https://github.com/NVIDIA/aicr/releases/tag/v1.1.2),
licensed Apache-2.0 like this repository. Tests check every digest below before use.

| Fixture | Source | SHA-256 |
|---|---|---|
| `aicr-attestation.sigstore.json` | member of `aicr_1.1.2_linux_amd64.tar.gz` | `57a1cb3771174c07dd38ae11172027400d6d22643f38f6083bcf73ecf7c8c91f` |
| `aicr_1.1.2_linux_amd64.sbom.json.gz` | gzip of `aicr_1.1.2_linux_amd64.sbom.json` | decompressed: `f56130e11d37bcd24031fcf7da317260e53392dfbbbcfceaecaeb8f0472df403` |

- Archive: <https://github.com/NVIDIA/aicr/releases/download/v1.1.2/aicr_1.1.2_linux_amd64.tar.gz>,
  `sha256:6350c5a50e7866569ec746b87b4c1c5e836f9ffba2b9505eb24241852f52e350`.
- SBOM: <https://github.com/NVIDIA/aicr/releases/download/v1.1.2/aicr_1.1.2_linux_amd64.sbom.json>.
- Both describe the `aicr` executable,
  `sha256:05c2c6ff0bec39d989a2b5c87930e7d57efdb82172b9f49fd2241b5ea9bc9c46`.
- The bundle is a Sigstore v0.3 DSSE bundle: in-toto Statement v0.1 carrying SLSA provenance v1, signed by
  `https://github.com/NVIDIA/aicr/.github/workflows/on-tag.yaml@refs/tags/v1.1.2` with issuer
  `https://token.actions.githubusercontent.com`.
- The SBOM is SPDX 2.3 JSON from syft 1.54.0. The fixture is gzip-compressed (`gzip -9 -n`) only to keep the
  repository small.
