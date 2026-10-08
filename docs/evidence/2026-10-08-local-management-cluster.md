# Real NKD-tool management Kind/CAPI replay, October 8, 2026

## Result and scope

The final reviewed `scripts/nke-local-management.py` completed its real
management-only diagnostic inside ARM64 Colima `nkd-demo`, Docker 29.5.2,
eight CPUs and approximately 24 GiB RAM. It consumed the existing completed
five-tool NKD Run, verified/extracted its bundle, checked client versions,
created one real Kind management node, installed NKE's pinned CAPI stage and
passed that stage's checks. It then automatically removed the exact node and
its newly created anonymous `/var` volume. The command exited 0.

This is not NKE site installation, CAPNICo/NICoSim tenant provisioning, QCOW2
profile boot, workload acceptance, full source-built distribution or Release
authority. No build command ran in the consumer. The acquired Kind node and
network-fetched CAPI/cert-manager runtime dependencies are not additional
NKD source builds or a qualified toolchain closure.

## Final replay identities

The script was exercised from the PR #118 implementation worktree based on
`1aa3a1625421c83c73669409e673b55f9bd74431`, plus this management increment.
Its exact exercised file SHA-256 was
`d21f069d36f1331c44e26f339904236883a0b1a2d75bee781b93b258f8d86893`.

Final diagnostic directory:
`/mnt/lima-colima-nkd-demo/nkd-local/management-diagnostic-20261008-04`.
Its private `inputs.json`, `node-ownership.json`, extracted tools, source
projection, kubeconfig and phase logs are retained outside Git.

| Subject | Exact identity |
| --- | --- |
| NKE installer source | `8fb2d53368823bd386b4fbd55b4ab6d3c0931345` |
| Existing tool Run | `laptop-colima-tools-arm64-20261008-01`, complete 1/1, unqualified local |
| Tool bundle manifest | `sha256:fb5d5575c7a7953937c6bfdef1fa2d2322fb6a1f1296265bfee7d12ec895dc19` |
| Tool Build | `sha256:53ae1075397a6dda4a66074c7f98ead32ac5ead0dfe80933bdcf4e00cbcc5caa` |
| Acquired Kind node | `kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5` |
| Consumer Kind configuration | `sha256:8551e19e1e06f6a34e198326b0006bf106e16735dfe75ae0bb35d2389efbf607` |
| Private cluster | `nker-demo-37f3ac6bcf80406d` |
| Deleted exact node ID | `c345aa1c73b9ad6ce496a049e60ab0510e3f38cf46852d48c327723447f5804b` |
| Deleted anonymous volume | `46ffaf2b5fec235b2272eebad713f0c6f1d16b9d6d39e990d47cd4934da82623` |

The actual command used separate source/output locations without rebuilding:

```sh
python3 -B \
  /Users/ikislenko/.codex/worktrees/main-laptop-os-build/nke-recipes/scripts/nke-local-management.py \
  --nke-checkout /mnt/lima-colima-nkd-demo/nkd-local/nke-installer-8fb-management-20261008-01 \
  --nkdctl /mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-ae535278b8afe70e07a69ddb51c86087773ac34f/.nkd/laptop-bootstrap/laptop-colima-tools-arm64-20261008-01/nkdctl \
  --tools-run /mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-ae535278b8afe70e07a69ddb51c86087773ac34f/.nkd/runs/laptop-colima-tools-arm64-20261008-01 \
  --out /mnt/lima-colima-nkd-demo/nkd-local/management-diagnostic-20261008-04
```

The clean native sparse installer clone came from an existing local pinned
source copy; the original NKE development checkout was untouched. The consumer
read exact Git blobs, projected stage 04, `deploy/lib.sh` and the tool-version
file into private storage, and executed that projection. Source SHA-256:

- Stage 04: `27fedc7e074fe171d52a040f670a05466c15b67213ff30351f87f3fadb6b8a4a`.
- Deploy library: `fd989e70f42bf6f4f1e76195f16502821e814d2d87aa6e598f7accda4f44f67a`.
- Tool versions: `0a4a74197a07a862faf2d536d8565bf8cce3cbc50b72d50bc7c0a67514af2f64`.

The source Kind fixture's exact Docker socket mount was removed under its
file-hash guard; the node image, dual-stack and ImageVolume settings stayed
unchanged. This consumer has no CAPD tenant. Kind's privileged node/container
runtime remains an operator-trusted diagnostic host boundary.

## Terminal checks and cleanup

The actual clients reported Kind 0.32.0, clusterctl 1.12.3, Helm 4.2.4,
kubectl 1.36.0/Linux ARM64 and ORAS 1.3.3/Linux ARM64. Wrong version/platform
observations fail before Kind creation. Bundle filenames alone are insufficient.

NKE's stage reported all six cert-manager/CAPI provider Deployments Available,
the Cluster CRD Established, CAPI 1.12.3 and cert-manager 1.19.3. Its cert-manager
version mismatch semantics remain warning-only; this replay reported the
intended version. No stronger server payload-digest or runtime qualification
claim is inferred from those version/readiness checks.

Before create, the random cluster identity was absent and initial volume
inventory empty. Cleanup checked exact node label/name/image, anonymous
local `/var` ownership, no pre-existing/shared volume or foreign consumer,
then used exact captured IDs with `docker rm --force --volumes` and checked
disappearance. All phase process groups were stopped/joined before cleanup.
Private ownership/cleanup logging is best-effort so full diagnostic storage
does not prevent owned-resource removal. Post-replay `docker ps -a` and
`docker volume ls` were empty. The shared Kind network and acquired image
cache remain; no global prune or default Mac context change ran.

The private directory was mode 0700 and kubeconfig 0600. The pinned installer
checkout stayed clean. No credentials/kubeconfig contents were published.

## Earlier probes, corrections and remaining work

Replay 01 installed/checked CAPI and removed its node, but left the anonymous
volume. Its exact volume was subsequently removed after Docker's matching
container-destroy and volume-unmount events established ownership. It is not
evidence of automatic complete cleanup.

Replay 02 passed CAPI but deliberately failed closed during volume admission:
Docker represents an anonymous `/var` in `Config.Volumes`, not
`HostConfig.Binds`. Actual node/volume inspection corrected that check; exact
cleanup was then exercised separately. Replay 03 passed automatically. Replay
04 above additionally exercises the final diagnostic-write-safe cleanup code.
Earlier script hashes are not substituted for the final one.

Independent review findings were fixed: descendants surviving exited leaders,
anonymous-volume retention, foreign client versions, hidden working-tree
source changes and cleanup blocked by diagnostics. Fourteen Python contracts
passed on the native Linux host, including actual descendants that ignore TERM, KILL escalation,
no cleanup after failed join, foreign/shared ownership, hidden stage projection,
wrong client versions/platforms and failed cleanup logging. The Go wrapper's
targeted race run passed. The macOS uncached suite initially hit a permission
error in the orphan-group fixture. That real orphan test is now Linux-only,
matching the consumer's runtime admission; macOS runs the other thirteen
contracts and explicitly skips that fixture. No production cleanup check was
relaxed. Fixtures are separate from this real replay.

After that fixture-scope correction, the uncached `go test ./... -count=1`
passed, as did `make check` (including lint, shellcheck and vulnerability scan),
the targeted management/VM-probe race tests, `make build` and staged
`git diff --check`. These repository gates are not full-demo runtime evidence.

Still required for the goal: integrate completed artifact selections and
PR #113's direct-source OS graph where applicable; serve the exact QCOW2 to
the real profile; make all selected images reachable by management/tenant
nodes; wire local NICoSim VM networking, bootstrap trust and the source-owned
site installer; create/verify the QCOW2-backed tenant and workload; then prove
the complete owned teardown. Missing/private-source exceptions and failed
full OS smoke remain explicit. No full-demo completion is claimed here.
