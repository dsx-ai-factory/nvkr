# APT capture byte-correspondence diagnostics

This record is an unqualified local diagnostic, not an NKE installer bake,
acquisition-origin attestation, live-Run receipt, installed inventory or disk SBOM.
The original source acquisition exercise used NKE
`f1eddbf62ff5866d593a8eb05d8eb2fcac80be1c`, as attributed by the original diagnostic
record. The source candidate is [dsx-ai-factory/nke PR #1211](https://github.com/dsx-ai-factory/nke/pull/1211).
This attribution is historical, not acceptance of that PR's latest revision.

## Captured bytes

| Target | Execution | Archive digest | Archive bytes | Manifest blob SHA-256 |
| --- | --- | --- | --- | --- |
| ARM64 | Native Ubuntu 26.04 container APT | `sha256:684ea432b9002ebb14fefcf81920e644dbd92d3afa7de8b24636f203d9e5504c` | 30,720 | `sha256:37eec045cbe8c04ce08e14f8692c6e2ba49d584759945c4d6c5501422a40ec88` |
| AMD64 | Emulated Ubuntu 26.04 container APT | `sha256:4a23f41797a0a762a410f45d5725ca65e76377face354d8e62a67d82b05cc0f5` | 30,720 | `sha256:d5162a0fbd87b1bdcb22a1eb8398f1910d7a696f79ea06e92295fc5c3446ab85` |

Each capture describes one actual `hello` package, version `2.10-5build1`.
The ARM64 package has 25,642 bytes and digest
`sha256:8ab9e5cf329e88413dd502edd5e0907550132470ecb926d500d0d23a30c735d8`.
The AMD64 package has 26,068 bytes and digest
`sha256:b6e9bef3c865aa17757cd9ffa820c05a37833f8a6b58287afc9f32dedb345965`.

## Independent verification

The original 2026-10-03 recheck did not retain a tested verifier revision, so it
cannot substantiate acceptance of a particular verifier revision. Historical
test-count and gate claims without committed logs are not used as evidence here.

On 2026-10-05, `TestObserveRealAcquisitionArchive` reopened the same retained
captures through `heldfile.OpenContext` and checked them with
`ObserveAcquisitionArchive`. Both rechecks passed with the exact values above.
The worktree base was NKD `71a8a359c57f19c473f6eaee7b8f9e375ac10b4a`, with local
review fixes. The tested `pkg/nkd/material/acquisitions.go` Git blob was
`2efa85692b96055186e618b5ea144b641f7b48f9` (`git hash-object`); this is the verifier,
not the earlier main baseline which lacks it. The [annotated test output for both rechecks](2026-10-05-apt-capture-recheck.log)
is retained. The archive files themselves remain local, not Git/CI artifacts.
These checks do not invoke APT, dpkg, Packer or QEMU and do not prove an original
installer-to-target capture handoff.

Ordinary CI also checks a retained Python 3.13.7 `tarfile.USTAR_FORMAT` fixture
using `TestObservePythonTarfileCapture`. Its archive is 10,240 bytes, SHA-256
`100cbc4b0354f184c7d0e81f3bf67805870074d272fccf63778a8f8669667f03`, and manifest-blob
SHA-256 `e05865caa150eff0441b9f78f60e5195ad9928ada62c841dce993546b11dc118`.
The [fixture and standard-library generator](../../pkg/nkd/material/testdata/README.md)
are retained with exact assertions. This is cross-language format coverage with
synthetic bytes, not a real Debian package or capture-enabled NKE bake.

For an independent recheck, supply a capture having one of the exact digests
above and run the diagnostic test with verbose output:

```sh
NKD_TEST_ACQUISITION_ARCHIVE=/path/to/capture.tar \
  go test ./pkg/nkd/material -run TestObserveRealAcquisitionArchive -count=1 -v
```

Compare the logged archive digest, size, manifest-blob SHA-256, architecture and
package count with this record; the generic diagnostic does not assert these
historical values automatically. A different
capture establishes its own byte correspondence, not reproduction of these
observed bytes. A future retained CI acquisition artifact and original NKE bake
are still required for shared operational evidence.
