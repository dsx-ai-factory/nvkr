# Direct public-source ARM64 build evidence

Recorded 2026-10-07. Scope: an unqualified native Linux source-build diagnostic, not a complete OS-image, release or TPM hardware test.

## Inputs and route

- Repository baseline: `d6cdf8dabb6bbda3af925ddb49eb16cde3eae8d9`; uncommitted experimental catalog later carried by the source-build PR.
- Upstream SPIRE TPM source: `https://github.com/spiffe/spire-tpm-plugin`, tag `v1.9.0`, commit `6eb314139fdeceb0332de617b4c90c7c63d60bc1`, tree `232f8514d5a27e0ad539f136702cfe7e558f6cd0`.
- Native ARM64 compiler: `docker.io/library/golang@sha256:27da97ffaed4bbb1263c44a8e423a72a1547830ee14a70fdb0cef3f6268a1b8b`.
- Existing Linux `aarch64` Podman machine, Podman 5.6.2, SELinux enforcing. No KVM is used for a binary compilation.
- CLI compiled with the repository's Go 1.27.1 for Linux ARM64 and executed inside that VM against the mounted isolated worktree.

The source lock was produced by `nkdctl catalog bump --all` with exact version constraints. The source/module preparation lane had network access. It verified/vendored the Go modules and rejected modification of the upstream module manifests. The producer containers compiled offline from authenticated read-only source with separate output directories.

## Command

The native VM workspace `/var/tmp/nkd-direct-tpm-20261007` was dedicated to this diagnostic. An initial run hit a SELinux bind-mount denial. The workspace was relabeled using a standard Podman shared `:z` bind mount and its `tmp` child selected as `TMPDIR`. SELinux remained enforcing; no global policy or executor behavior was changed.

```sh
mkdir -p /var/tmp/nkd-direct-tpm-20261007/tmp
podman run --rm --network=none \
  --volume /var/tmp/nkd-direct-tpm-20261007:/workspace:z \
  docker.io/library/golang@sha256:27da97ffaed4bbb1263c44a8e423a72a1547830ee14a70fdb0cef3f6268a1b8b true

TMPDIR=/var/tmp/nkd-direct-tpm-20261007/tmp \
timeout --kill-after=15s 900 bin/nkdctl-linux-arm64 build \
  --data definitions/nke-os-source/catalog \
  --component tpm-attestor-agent-1-9-0 \
  --component get-tpm-pubhash-1-9-0 \
  --platform linux/arm64 --runtime podman \
  --source-root /var/tmp/nkd-direct-tpm-20261007/src \
  --jobs 2 --skip-preflight \
  --emit /var/tmp/nkd-direct-tpm-20261007/observed.json
```

`--skip-preflight` skips the qualified-profile host preflight for this development diagnostic. It does not make the result qualified or bypass source/output integrity checks. The command terminated successfully and emitted its terminal report, which was read back from the VM.

## Observed results

| Producer | Bytes | SHA-256 |
|---|---:|---|
| get-tpm-pubhash-1-9-0 | 3,670,140 | `2d9b1ebbcceb220d4d6feb74aab438ab0a53541f4008e935495b1b9ad442f788` |
| tpm-attestor-agent-1-9-0 | 12,386,428 | `2e01a325bf63ec86395f4d7e01f53f4b1835cffcc81009759cde962d40d21e9f` |

The recorded two-producer Build Digest was `sha256:53cd9e2f1e1f9b887d5a4f26884cdac369b8caa1aaf3f49ee0cad27c73ee4a09`. Both terminal results bind source input `sha256:4c740c31b186b4bd74639eb1e0cd05b87241c8beb40ca1876e7ab3861c79f7e9` and the ARM64 compiler digest above. Adding producers or changing commands changes the selected graph and its Build Digest; this value is not an expected digest for the later all-producer test.

The terminal phase logs reported approximately 9.1 seconds for source/module preparation, 4.4 seconds for the helper build and 6.6 seconds for the plugin build. These timings are warm local-machine observations, not CI resource budgets.

## Complete declared ARM64 source-producer test

A subsequent fresh integration run used the final 14-producer catalog, all 13 source checkouts, the same native ARM64 compiler and Podman VM, with no component selection override:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c \
  -o bin/nke-os-source.test ./pkg/nkd/buildgraph

# Inside the native VM, from pkg/nkd/buildgraph in the mounted worktree:
TMPDIR=/var/tmp/nkd-direct-live-20261007/tmp \
NKD_OS_SOURCE_RUNTIME=podman ../../../bin/nke-os-source.test \
  -test.run '^TestNKEOSPublicSourcesLive$' -test.v -test.timeout 75m
```

The dedicated `/var/tmp/nkd-direct-live-20261007` parent was prepared with the same shared `:z` labeling procedure. Go's test owner created fresh source/output temporary directories below it. Source preparation verified/vendored the non-vendored sources; all producer containers compiled with `--network=none`. Kubernetes used its pinned upstream `go.work` and non-debug `selinux,notest,grpcnotrace` tags. Helm preparation checked and removed only named external negative-test fixture links; the production held-vendor admission limitation remains in the catalog README.

The test passed all 14 subtests in **403.08 seconds**. It rehashed every terminal output, checked exact tar membership, inspected raw ELF architecture and successfully executed version probes for all three crictl and both Helm versions. No service, Kubernetes cluster or TPM hardware probe was performed.

Exact inputs to this diagnostic:

- Registry SHA-256: `532fd4cf8d88cdf2dc3751e11b1f3dc549d76fccf25a913d35be3ea0b4b94320`.
- Source lock SHA-256: `6aa73156f8d88f97bfcc1f1da46626e5d80d8b3f5c99ec7eddaf9d9d014bb07f`.
- Integration test source SHA-256: `80f2fa7d1b19264c570016e537716bb89eb1fc8fe8aeb8f4b696bb89b90fae65`.

| Producer | Bytes | Observed SHA-256 |
|---|---:|---|
| cni-plugins-1-6-0 | 73,093,120 | `f4252dd6ee7aab47688b6f70236e88fe014e91fb492b72cd2c276337074861ab` |
| containerd-2-1-7 | 69,345,280 | `25f2f11363f70bdd65ebb73f9290500e7c130f304de3c6f70f8498b6acff5171` |
| containerd-2-2-3 | 72,294,400 | `ffd42cb0a298761f3bca0619979c09f9b1d8a57370f08dbd2cc769c2d21461fc` |
| containerd-2-3-0 | 65,413,120 | `d2656b993ccc309112100d89c52cf21b6e185de22eba08ad2bac0f9f0aaba76f` |
| crictl-1-34-0 | 26,804,348 | `5f1df79bb130d07810fd25f3a0a0c1ac965e4a54bd29db0f8dea5f2681f0664d` |
| crictl-1-35-0 | 26,869,884 | `8db912e6fb0dc0ea92eeb78fdf9d16e1eecd1e09105f98ca3e59e34923ab9add` |
| crictl-1-36-0 | 26,542,204 | `6e5368bff3e99490a4031106547111eb80b7b5f930ba7eb5377679f97947b9eb` |
| get-tpm-pubhash-1-9-0 | 3,670,140 | `2d9b1ebbcceb220d4d6feb74aab438ab0a53541f4008e935495b1b9ad442f788` |
| helm-3-20-2 | 54,067,324 | `496d7bf29bed2003b53eadfb82c25dcc126df76045ea93dc416cdbced68ccfe0` |
| helm-4-1-4 | 58,851,452 | `d5b9e3f57bd8f3049b87584b07073806eb9c943455d9f3c83d5a6a9116792dd8` |
| kubernetes-1-34-2 | 513,361,920 | `18f781ccfd607be7c147610090d1b7231e9b9229324b08948f55d5f52b341fe1` |
| kubernetes-1-35-3 | 505,948,160 | `e16bf7037a35c69c32156e6044cb5a2e1f3d16a5f1bf4cfeaeb0582800ea5e25` |
| kubernetes-1-36-3 | 412,364,800 | `be35718d51a4e94226f54b199aac8ba482c5a3b4475d141a259afd2a52c0557e` |
| tpm-attestor-agent-1-9-0 | 12,386,428 | `2e01a325bf63ec86395f4d7e01f53f4b1835cffcc81009759cde962d40d21e9f` |

These are measurements of this diagnostic, not expected upstream release download hashes or an authenticated release receipt.

## Repository gate

`make build`, focused graph contracts with `-race`, `actionlint`, `yamllint` and `git diff --check` passed. The full `make check` passed with Go 1.27.1 and the already-installed Bash 5 selected on `PATH`, including the complete race/coverage suite, vet, lint, shellcheck, Python CI-tool tests and the Go vulnerability scan. Coverage was 71.6%; the scanner reported no vulnerabilities in the NKD module scope.

The first full-gate attempt used macOS Bash 3.2 and failed three existing `toolchains/nkd-packer` export/load cases at empty-array expansion. Rerunning with Bash 5 passed those same tests without changing them or the toolchain script. The final gate did not skip or disable tests. This module scan does not establish vulnerability or license review of every experimental upstream component/toolchain.

## Limits

No AMD64 source execution, Debian installation, TPM attestation, complete NKE OS component closure, embedded image assembly, Packer bake, publication, signing or release acceptance is established by these local runs. They do not qualify compiler licensing/security or assert byte reproduction. The DSX IMDS payload exception and its inaccessible release URLs remain unresolved for a complete image.

The PR adds a separate integration test and native ARM64/AMD64 CI jobs for all declared public-source producers. Their live results must be inspected independently; this document does not predeclare those jobs successful.
