# Direct public-source native build evidence

Recorded 2026-10-07. Scope: an unqualified native Linux source-build diagnostic, not a complete OS-image, release or TPM hardware test.

## Inputs and route

- Repository baseline: `d6cdf8dabb6bbda3af925ddb49eb16cde3eae8d9`; uncommitted experimental catalog later committed in `aa8e06455016b3bb1431fc2aba23cd43140a517d`.
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

The original local runs establish ARM64 execution only. The hosted records below additionally establish AMD64 source execution. Neither establishes Debian installation, TPM attestation, complete NKE OS component closure, embedded image assembly, Packer bake, publication, signing or release acceptance. They do not qualify compiler licensing/security or assert byte reproduction. The DSX IMDS payload exception and its inaccessible release URLs remain unresolved for a complete image.

The hand-authored hosted workflow executes the unqualified `pipeline.Local` diagnostic, not the production held factory Run route. In particular, Helm's pristine external fixture links remain rejected by held `ExecNode` vendor admission. Neither a green source diagnostic nor its raw binaries closes that production integration prerequisite.

## First hosted Docker attempt

[Run 37675257555](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37675257555) tested PR head `aa8e06455016b3bb1431fc2aba23cd43140a517d`. The ARM64 job failed during Helm preparation: Git reported dubious ownership of `/src`, then test cleanup could not remove root-owned vendored files. This is not a failure of the recorded rootless Podman run and is not a successful hosted source build.

The diagnostic workflow was changed to compile the same test binary as the runner user and execute it as root on the ephemeral credential-free Docker runner, matching the rootful daemon's file ownership. No global Git safe-directory exception, source-validation weakening or production executor change was made. The first attempt is retained as failure evidence; the corrected passing attempts are recorded below.

## Completed hosted runs

[Run 37676134544](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37676134544) at `a7d81d085c2417ab73da40526e67315782836012` passed all 14 producers on both architectures. Job wall times were 40m25s AMD64 and 26m50s ARM64. This test predates bundle-member ELF validation.

[Run 37685111404](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37685111404) at `8d4519c2604dd13266f82f4033b9cfd8c0134840` failed ARM64 during network module acquisition with a Go proxy HTTP/2 stream error. It is not a passing rerun.

[Run 37690165034](https://github.com/dsx-ai-factory/nke-recipes/actions/runs/37690165034) at `78bd9a9ecb60f341654bb41d1af1938ad0b666ec` passed both architectures with exact membership and per-member ELF validation. AMD64 live test took 2319.97s (job 39m51s); ARM64 took 1473.25s (job 25m21s). Test source SHA-256 was `ef31d86e7db3d05eb193226b816348b9f50004841ff8817ed4239b21ab63ca1a`. This records that exact historical head; later stamp/probe changes need their own evidence.

| Architecture | Producer | Bytes | Observed SHA-256 |
|---|---|---:|---|
| amd64 | cni-plugins-1-6-0 | 76830720 | `ed54c3c005efa9b5e43894de5237872d404c96bfbcb5cfdc8129bbe29da0921f` |
| amd64 | containerd-2-1-7 | 74291200 | `ed8882d711702f9e16cf42872157b8f7e47d344596bc5fbdf33b0b7d08688fb6` |
| amd64 | containerd-2-2-3 | 77424640 | `981136681d51b9a82c5b99348fca85da227065e80e40b2bd575133891a969e3d` |
| amd64 | containerd-2-3-0 | 70113280 | `467dcf95f8a1f8b88c04b2f66a6f5a0d20bf1a1f8edaeacfc4dc09cb023361b7` |
| amd64 | crictl-1-34-0 | 28639356 | `e0edca3032f0f70127006a0f071cd5036617e746cc4544692ff4b52c21fdf4df` |
| amd64 | crictl-1-35-0 | 28782716 | `7461288c7e72ff8438d96709748eebd45b17240859a6ea767be01bc80cac52fe` |
| amd64 | crictl-1-36-0 | 28422268 | `5a277761b3f7cf6d55f8a864c0765ec1ddcb622f17faa5f5561599d81b76f80c` |
| amd64 | get-tpm-pubhash-1-9-0 | 3940476 | `0c44b39210b2a9bb97588b3d063c4afba0303dbcb971ce5358604e48eb463cc0` |
| amd64 | helm-3-20-2 | 58212476 | `cd6ce3c9793af4e3ebca93aa8e7d7277e1473c7a7aba3db9c0059fe3450a105d` |
| amd64 | helm-4-1-4 | 63352956 | `f46f969407cc2c6f6409a8d834092b22a08b070eca88476048ab8c3f3e584fe2` |
| amd64 | kubernetes-1-34-2 | 552314880 | `098b70cf5dc634939ac794f3394e01ddcd35bcdd5968e95400c62468a36117fd` |
| amd64 | kubernetes-1-35-3 | 545382400 | `9ceb4521c51dacb12ae1b67806405ce3a52f7d69267220b472c7d5f694c805ec` |
| amd64 | kubernetes-1-36-3 | 445224960 | `4f3717baa3e89d1f0070f3c6ea12247961ee3bf3f3c8971208fc626c83fdaf46` |
| amd64 | tpm-attestor-agent-1-9-0 | 13312124 | `48bfdb9dc78c772deb90a8b9431ed0d8d6b1af8e6869a585441ea8e94791a87c` |
| arm64 | cni-plugins-1-6-0 | 73093120 | `f4252dd6ee7aab47688b6f70236e88fe014e91fb492b72cd2c276337074861ab` |
| arm64 | containerd-2-1-7 | 69345280 | `25f2f11363f70bdd65ebb73f9290500e7c130f304de3c6f70f8498b6acff5171` |
| arm64 | containerd-2-2-3 | 72294400 | `ffd42cb0a298761f3bca0619979c09f9b1d8a57370f08dbd2cc769c2d21461fc` |
| arm64 | containerd-2-3-0 | 65413120 | `d2656b993ccc309112100d89c52cf21b6e185de22eba08ad2bac0f9f0aaba76f` |
| arm64 | crictl-1-34-0 | 26804348 | `5f1df79bb130d07810fd25f3a0a0c1ac965e4a54bd29db0f8dea5f2681f0664d` |
| arm64 | crictl-1-35-0 | 26869884 | `8db912e6fb0dc0ea92eeb78fdf9d16e1eecd1e09105f98ca3e59e34923ab9add` |
| arm64 | crictl-1-36-0 | 26542204 | `6e5368bff3e99490a4031106547111eb80b7b5f930ba7eb5377679f97947b9eb` |
| arm64 | get-tpm-pubhash-1-9-0 | 3670140 | `2d9b1ebbcceb220d4d6feb74aab438ab0a53541f4008e935495b1b9ad442f788` |
| arm64 | helm-3-20-2 | 54067324 | `496d7bf29bed2003b53eadfb82c25dcc126df76045ea93dc416cdbced68ccfe0` |
| arm64 | helm-4-1-4 | 58851452 | `d5b9e3f57bd8f3049b87584b07073806eb9c943455d9f3c83d5a6a9116792dd8` |
| arm64 | kubernetes-1-34-2 | 513361920 | `18f781ccfd607be7c147610090d1b7231e9b9229324b08948f55d5f52b341fe1` |
| arm64 | kubernetes-1-35-3 | 505948160 | `e16bf7037a35c69c32156e6044cb5a2e1f3d16a5f1bf4cfeaeb0582800ea5e25` |
| arm64 | kubernetes-1-36-3 | 412364800 | `be35718d51a4e94226f54b199aac8ba482c5a3b4475d141a259afd2a52c0557e` |
| arm64 | tpm-attestor-agent-1-9-0 | 12386428 | `2e01a325bf63ec86395f4d7e01f53f4b1835cffcc81009759cde962d40d21e9f` |

The original registry/lock/test hashes above remain measurements of the earlier local run, not refreshed hashes falsely attributed to a later test. Rebuilt candidate identity and feature limits remain in the catalog README.

## Review-fix native ARM64 rerun

On 2026-10-07 the final corrected test ran on the same native ARM64 Podman VM
with SELinux enforcing and the dedicated temporary parent described above. It
passed all 14 producers and subtests in **367.41 seconds**, with terminal exit 0.
All compilation containers used the pinned Go 1.27.1 image and offline compilation.
The tests additionally checked Go build information, all Kubernetes/containerd
bundle-member versions, CNI protocol/build versions and Helm 3 template/lint
Kubernetes defaults (both observed `v1.35.0`). No services or clusters were started.

Exact tested inputs:

- Registry SHA-256: `6b63bbe69463b9e4039004463bb5cc7a63f1b20659012de771e8e3f59b5753a8`.
- Source lock SHA-256: `6aa73156f8d88f97bfcc1f1da46626e5d80d8b3f5c99ec7eddaf9d9d014bb07f` (unchanged).
- Test source SHA-256: `10313e6c8b8f4abc6eea5be96a8d8768f5a37b58b0909996ec141fe93533fb94`.
- Linux ARM64 test executable SHA-256: `8a6cd05d742e4f7b8acf6de59c6e9f8846c392ea713f97e234f2c028e502e1f5`.

| Producer | Bytes | Observed SHA-256 |
|---|---:|---|
| cni-plugins-1-6-0 | 73093120 | `281b7a5b141d8c73f2593fe3331134ab52a1a73fd5758e0ba8402ae34dfe6eb1` |
| containerd-2-1-7 | 69345280 | `d17cc3859f879704eba61e2156d1a30109d4c5094b63e70d60fb240827bbbd24` |
| containerd-2-2-3 | 72294400 | `468274f1247540b426a64039f3ae231f1986a8671d3ca18afe788df1010afdfa` |
| containerd-2-3-0 | 65413120 | `0dc69b9c3c3cd3974f821950f135c85b5450439b5b396ef27ceadfc8fa3d4530` |
| crictl-1-34-0 | 26804348 | `5f1df79bb130d07810fd25f3a0a0c1ac965e4a54bd29db0f8dea5f2681f0664d` |
| crictl-1-35-0 | 26869884 | `8db912e6fb0dc0ea92eeb78fdf9d16e1eecd1e09105f98ca3e59e34923ab9add` |
| crictl-1-36-0 | 26542204 | `6e5368bff3e99490a4031106547111eb80b7b5f930ba7eb5377679f97947b9eb` |
| get-tpm-pubhash-1-9-0 | 3670140 | `2d9b1ebbcceb220d4d6feb74aab438ab0a53541f4008e935495b1b9ad442f788` |
| helm-3-20-2 | 54067324 | `f98666c195569e40b2f8efb6a30184b39eb6bda25ca10fbc647babfc36cf8419` |
| helm-4-1-4 | 58851452 | `d5b9e3f57bd8f3049b87584b07073806eb9c943455d9f3c83d5a6a9116792dd8` |
| kubernetes-1-34-2 | 513423360 | `6bdbe875e655b21e9454ff25de9394736ab66a8cdfcfbbd6e3a2fc217f0c2417` |
| kubernetes-1-35-3 | 505948160 | `7e6b91dcbb85dae159d16eaaaab87f3c9e028d688f23d93918e8ee67d732aa6e` |
| kubernetes-1-36-3 | 412364800 | `0e112f911970015fccc1e11a8471b6ce8161f7d23cd21f787e6d25c3e23b9621` |
| tpm-attestor-agent-1-9-0 | 12386428 | `2e01a325bf63ec86395f4d7e01f53f4b1835cffcc81009759cde962d40d21e9f` |

An intermediate native attempt built all 14 outputs but failed probe checks in
398.79 seconds. The test incorrectly expected Go build information to retain
linker flags under `-trimpath`, and used `--version` instead of the supported
`-v` for older containerd shims. The corrected test checks compiler identity
through standard Go build information and version stamps through native commands;
no compiler reproducibility setting or production source check was relaxed.
That failed attempt's test source hash was
`d46dd59c5dd3dca9e972fed6177db8db93e17b161771fc80369f03fa59261871`.

This final evidence is ARM64 only; a fresh hosted AMD64 run of these review fixes
is still required. All diagnostic, production-admission, feature and acceptance
limits above remain unchanged.
