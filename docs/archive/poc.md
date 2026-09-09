# nkdctl POC — historical vertical slice

> [!CAUTION]
> **Historical demonstration snapshot.** The commands, results, and support claims below describe the POC at
> the time they were recorded. They are useful reproduction notes, not current release acceptance evidence or
> a supported-interface contract. The proposed ADRs in `docs/adr/` are the maintained engineering baseline
> pending ratification, and `docs/adr/reconciliation.md` records the current implementation boundary and
> evidence level.

This POC implements one end-to-end path through the nkdctl architecture:

```
criteria  ──►  resolve (embedded catalog)  ──►  pinned build IR (recipeDigest)  ──►  local hash-pinned
                                                                                    toolchain build engine
                                                                                    (the "CI target")
```

It builds the **Kubernetes core** — `kubelet` and its siblings (`kubeadm`, `kubectl`, `kube-proxy`,
`kube-apiserver`, `kube-controller-manager`, `kube-scheduler`) — from one pinned upstream source tree,
hermetically, in a digest-pinned Go toolchain container.

## What's pinned (the whole trust chain is real values, not placeholders)

| Input | Pin |
|---|---|
| Source | `github.com/kubernetes/kubernetes`, tag `v1.32.0` → commit `70d3cc986aa8221cd1dfb1121852688902d3bf53` |
| Source tree hash | `195abcc8a74b362b88fc76b0ee676a3354e4937e` (verified at fetch) |
| `SOURCE_DATE_EPOCH` | `1733939955` (the commit's committer `%ct`) |
| Toolchain | `sha256:220f97938c78…` — `toolchains/go/Dockerfile` = `golang@sha256:59b8183301af…` (Go 1.23.3) + rsync |

The commit is verified after fetch (commit **and** tree hash must match, or the run fails closed). k8s vendors
its dependencies, so the compile runs with `--network=none`.

## Run it

```sh
make build                                           # -> bin/nkdctl (embeds the default catalog)
make test                                            # pure-logic unit tests; no Docker needed

# resolve + inspect (no Docker)
bin/nkdctl catalog list
bin/nkdctl recipe --category kubernetes --role control-plane --k8s 1.32.0 --os ubuntu --arch amd64          # pinned IR + recipeDigest
bin/nkdctl recipe --category kubernetes --role control-plane --k8s 1.32.0 -o digest                         # just the digest
bin/nkdctl bundle --engine github --category kubernetes --role worker --k8s 1.32.0                          # emit GitHub Actions
bin/nkdctl bundle --engine local  --category kubernetes --role worker --k8s 1.32.0                          # local execution plan

# build the per-arch toolchains once, then build from source (needs Docker + git + network for fetch)
docker build --platform linux/amd64 -t local/nkd-toolchain-go:amd64 toolchains/go    # kubernetes lane
docker build --platform linux/arm64 -t local/nkd-toolchain-go:arm64 toolchains/go
bin/nkdctl build --category kubernetes --role worker --k8s 1.32.0 --arch arm64 --only kubectl               # one component, native arm64
bin/nkdctl build --role control-plane --k8s 1.32.0 --arch amd64                       # the whole core
```

Fail-closed paths each return a distinct coded exit status, e.g.:

```
bin/nkdctl recipe --role worker --compliance fips   # [NKD-E-RESERVED-AXIS]  exit 12
bin/nkdctl recipe --role worker --arch riscv64      # [NKD-E-UNSUPPORTED]    exit 13
bin/nkdctl recipe --role worker --k8s 9.9.9         # [NKD-E-NO-MATCH]       exit 10
```

## Verified results

Complete build — `nkdctl build --role control-plane --k8s 1.32.0` — all seven core components compiled from
one pinned source tree in the digest-pinned toolchain under `--network=none`. The terminal verify node re-hashes
each artifact against its build-time digest (failing closed on any mismatch). These are content hashes of the
raw compiled binaries — packaging to `oci-image`/`.deb` is deferred, so they are **not** yet image/package
digests:

> **Reproducibility-flag hardening (later pass):** the flag discipline is now pinned **in the IR** —
> `GOFLAGS=-trimpath -buildvcs=false` (guarded so a catalog can't override it) plus per-lane
> `-ldflags=-buildid=` on the direct `go build` components. This makes `recipeDigest` (now
> `sha256:238c534d3714b25cfe536b5bfa4902958ced5dbe2c2fcb45b0f85ba65f3e5b39`) provably carry the discipline for
> every Go lane. The raw-binary digests in the table below are from the **pre-hardening** run; a rebuild under
> the current catalog is expected to shift the direct-`go-build` lanes (etcd/coredns/containerd/crictl/helm/runc)
> as VCS stamping is dropped — re-measuring them is the natural follow-on.

| Component | Output form | Lane | sha256 | Bytes |
|---|---|---|---|---|
| kube-apiserver | oci-image | static (CGO off) | `6a7a8d3ad88a85f6…` | 93,237,400 |
| kube-controller-manager | oci-image | static | `2488c202c7430a67…` | 85,975,192 |
| kube-proxy | oci-image | static | `64e504bceae7c8d8…` | 66,822,296 |
| kube-scheduler | oci-image | static | `3b24e127f79bcfd1…` | 65,835,160 |
| kubeadm | host-package | static | `b7c62f0e1c7912a3…` | 70,942,872 |
| kubectl | host-package | static | `6ac95d99c1a422d5…` | 57,323,672 |
| kubelet | host-package | **CGO** (amd64 cross gcc) | `4714ce089a440ea1…` | 77,398,276 |

Each is a real `ELF 64-bit x86-64` binary stamped `v1.32.0`; the six pure-Go ones are statically linked and
stripped, kubelet is dynamically linked (its CGO lane, like runc).

Reproducibility signals observed:

- **Same-environment byte-identity:** kubectl produced the identical `sha256:6ac95d9…` across three
  independent runs (fresh `--rm` containers, cleared output tree) — the reproducibility thesis, on a real k8s
  component.
- **Stable across a toolchain-image rebuild:** all six pure-Go digests were unchanged after the toolchain
  image was rebuilt (rsync → + cross-gcc). The image ID changed but the embedded Go compiler bytes did not, so
  the outputs did not — exactly the property the from-source thesis needs.
- **Deterministic `recipeDigest`:** identical across processes; changes when any pin changes.

**Two-environment bit-identity, post-hardening (the Phase-1 gate).** `nkdctl bake-off --only
runc,etcd,coredns,kubectl` rebuilds each component **twice** under deliberately different host environments
(different build path + `TZ`/`HOME`) on the **hardened catalog** (`GOFLAGS=-trimpath -buildvcs=false` +
`-ldflags=-buildid=`) and asserts byte-identity via `attest.VerifyAgainst`. All four reproduced **bit-identically**,
including the hard **runc CGO** lane:

| Component | Lane | sha256 (identical in both environments) |
|---|---|---|
| runc | static-CGO (glibc) + libseccomp | `sha256:54905d2eec0c8fbb…` |
| etcd | pure-Go, vendored, `-buildid=` | `sha256:417fb85189700170…` |
| coredns | pure-Go, vendored, `-buildid=` | `sha256:18bd23d818a6b4d6…` |
| kubectl | k8s `make` lane | `sha256:3cba7422b5c1f5cc…` |

> Honest scope: this is **within-machine, varied-environment** (two build paths + different host TZ/HOME on one
> host). The ultimate gate — the same builds on **two physically distinct machines** (SRD §5.10, NKD-OI-001) —
> is still the remaining step. But the reproducibility thesis now holds across environments *under the hardened
> flags* and on the CGO lane, which is where reproducibility is hardest.

## How the code is organized

| Package | Responsibility |
|---|---|
| `pkg/nkd/criteria` | four active axes, asymmetric match, specificity, reserved-axis fail-closed |
| `pkg/nkd/catalog` | `SourceBuildRegistry` types, `DataProvider` seam (embedded / `--data`), fail-closed validation |
| `pkg/nkd/ir` | engine-neutral build DAG, deterministic marshal, `recipeDigest = sha256(IR)` |
| `pkg/nkd/recipe` | resolve (criteria+catalog → recipe) and compile (recipe → IR) |
| `pkg/nkd/pipeline` | `Local` runner (executes the IR via Docker — the ground-truth oracle) + `GitHub` emitter (same IR → CI YAML) |
| `pkg/nkd/errors` | coded errors → exit codes |
| `catalog/` | the embedded default catalog (`registry.yaml`, via `//go:embed`) |
| `toolchains/go/` | the pinned, reproducible toolchain image |

## Now implemented (beyond the first slice)

- **Trust loop:** `attest` → expected-digests manifest; `verify` rebuilds and proves byte-identity, fail-closed
  naming the first divergent node; `--package` makes those real oci-image/.deb digests. cosign signing is
  **plumbed** (`attest --sign`, `verify --signature`) as an external subprocess hook.
- **Reproducible packaging:** deterministic OCI image, `.deb`, and rootfs **tarball** writers (`pkg/nkd/assemble`).
- **SBOM:** deterministic CycloneDX (`sbom`).
- **Three sources, all building hermetically:** kubernetes (7 core), **etcd** (Go 1.25; non-vendored → vendored
  in the fetch step), **runc** (the SRD's hard case: static-CGO + libseccomp). Both hard CGO lanes build. *Note:*
  the runc lane is **glibc-static** (GNU cross-gcc + static libseccomp), not musl — a true musl static lane is
  the SRD's stated target and the deferred hardening.
- **Pin maintenance:** sources + `pins.lock.yaml`, `catalog pin`/`bump`/`update-ci`, and toolchain digests
  **resolved from `.go-version`** so a Go-version bump carries its toolchain (docs/versioning.md).
- **Engines:** GitHub Actions, GitLab, Tekton, Argo emitters over the one IR; concurrent local DAG scheduler.
  Every emitter lowers the build to the exact `docker run --network=none` the local runner executes (so all four
  are byte-identical + hermetic), fetches/vendors in a separate network step, and ends in an **unconditional**
  verify (github `if: always()`, gitlab `when: always`, tekton `finally:`, argo finished-state `depends`).
- **Gate:** `bake-off` builds twice under varied host env and asserts byte-identity.
- **Multi-arch (native per-arch, model B) — all 15 components:** `arch` is a real axis
  (`archDomain={amd64, arm64}`, DefaultArch=amd64). Recipes are arch-agnostic (`arch: any`); the **same
  recipe** resolves for any arch and every build node carries the target `GOARCH` + `platform:
  linux/<arch>`. The runner and all four CI emitters run the toolchain under `--platform`, so a multi-arch
  (manifest-list) `golang` toolchain resolves to its **native** variant — the 6 pure-Go sources (etcd,
  coredns, containerd, cni-plugins, crictl, helm) go multi-arch with **zero catalog change** — while the
  CGO/C lanes pin a **native per-arch toolchain** (`Source.toolchainImagesByArch`: kubernetes = `toolchains/go`
  native gcc, runc = `toolchains/cgo` native gcc+libseccomp). No cross-compilation; the one per-arch build
  literal is the `{{.Arch}}` token, substituted at Compile (any other `{{…}}` fails closed at load). The
  control-plane recipe (all 15, now COMPOSED from the layered default) gives **distinct, stable
  recipeDigests** per arch — amd64 `sha256:f08265d2…`, arm64 `sha256:607bd0b7…` — and the arm64 IR contains **zero** `amd64`/`x86_64`
  literals. Bit-identity proven per component via `bake-off` (build twice, varied env), arm64 **native** and
  amd64 **emulated** (stand-in for a native amd64 runner):

  | component | lane | arm64 (native) | amd64 (emulated) |
  |---|---|---|---|
  | etcd | pure-Go, manifest-list toolchain via `--platform` | ✅ bit-identical | ✅ bit-identical |
  | coredns | pure-Go (vendor step) | ✅ bit-identical | — |
  | kubectl | kubernetes `make` lane (arch-templated) | ✅ bit-identical | — |
  | pause | kubernetes native gcc (C) | ✅ bit-identical | ✅ bit-identical |
  | runc | static-CGO + libseccomp | ✅ bit-identical | ✅ bit-identical |

  The full 15×2 native/emulated bit-identity sweep (esp. the heavy emulated-amd64 `make` lanes) is a
  CI-matrix job — one native runner per arch; here each arch's build is proven native-target + reproducible.

## No-compile (prebuilt) lane — `fetch` (verified live)

A deliberate, opt-in **parallel** to from-source (NKD-FR-BLD-050/051): acquire pinned, content-addressed
artifacts instead of building them — **same** fail-closed / deterministic / coded-error / one-IR-many-adapters
discipline, but a weaker, honestly-stated trust claim. From-source proves "the same inputs rebuild the same
bytes"; prebuilt guarantees only "exactly these pinned bytes." The pinned artifact digest is the
reproducibility anchor and is **part of `recipeDigest`** (a repin is a recipe change); an all-prebuilt manifest
is **profile-independent** (carries no builder-profile digest). A component is from-source **XOR** prebuilt
**XOR** an exclusion. The embedded default catalog stays 100% from-source; the lane is demonstrated in
`examples/operator-catalog` (`acme-prebuilt-node`).

- **Catalog + machine-managed pins.** A component declares `prebuilt: {url|image}` (version-templated,
  `{{.Arch}}` resolved at Compile); `nkdctl catalog pin-artifacts --all` resolved the per-arch digests into the
  lock's `artifacts:` section from **real sources** — dl.k8s.io `.sha256` sidecars (kubectl) and `docker
  manifest inspect` (pause):
  `kubectl` amd64 `646d58f6…`/arm64 `ba4004f9…`, `pause` amd64 `7c38f247…`/arm64 `e50b7059…`.
- **`fetch`** (`--role worker --k8s 1.32.0 --os ubuntu --arch amd64`) downloaded kubectl (57 MB) and pulled
  pause by digest, verifying each against the pin — verify node green. **Tampering** the pinned digest fails
  closed: `NKD-E-VERIFY-FAILED artifact digest mismatch … the pinned artifact was not served`, **exit 22**.
- **`attest` → `verify`** on the all-prebuilt recipe: manifest carries 2 component digests and **no
  profileDigest** (correct — acquisition is host-independent); `verify` re-acquired and printed **VERIFIED
  (Reproduced) … under profile (none)**.
- **`--package`** assembled the verified kubectl binary into a **reproducible `.deb`** (`sha256:c4f10e8d…`,
  identical across two runs).
- **All four CI emitters** lower a pull node to the same acquisition the local runner performs — `curl -fsSL …
  && echo "<digest>  <f>" | sha256sum -c -` for a binary, `docker pull <name>@<digest>` for an image — and the
  unconditional `verify` job waits on every pull job.

## Complete Tier-1 node substrate — built from source and boot-verified

All **15 Tier-1 components** (SRD Appendix B) build from source, hermetically: kube-apiserver,
-controller-manager, -scheduler, -proxy, kubelet, kubeadm, kubectl, **pause** (C), etcd, coredns, containerd,
runc, cni-plugins, crictl, helm — from **8 upstream sources**, spanning Go 1.23–1.25 (toolchains resolved from
`.go-version`), non-vendored repos (etcd/coredns/helm) vendored in the fetch step, both hard CGO lanes
(kubelet cross-CGO, runc static-CGO+libseccomp — glibc-static; musl is the deferred target) plus pause via
cross-gcc.

`nkdctl image` bootstraps a **reproducible base OS rootfs via mmdebstrap** (native foreign-arch amd64
bootstrap against a date-pinned Debian snapshot). `scripts/boot-node.sh` installs the from-source substrate
binaries into that rootfs, assembles an ext4 disk with `mke2fs -d` (no privileged mount), and **boots it in
QEMU** — the guest reaches userspace and the substrate runs: `kubelet -> Kubernetes v1.32.0`, `containerd
1.7.34`, `runc 1.5.1`, 18 CNI plugins present (see `.nkd/work/image/boot.log`).

## Reconciliation with NKD v1 (SRD v0.4 — favoring `nkd-v1.md`)

Five load-bearing positions were reconciled toward the NKD v1 product definition, each with a working code slice
(`docs/nkd-v1-vs-srd.md` has the full resolution table; `pkg/nkd/{profile,variant}`, `attest.EvidenceBundle`,
`assemble.LogicalDiskDigest`).

- **Reproducibility is bounded to a signed builder profile** (not host-independent). The profile *digest* is the
  identity; a fail-closed **host preflight** runs before inputs resolve. Live, on this arm64 host:
  ```
  nkdctl profile preflight --arch arm64   # PASS (advisory os/kernel divergences recorded)
  nkdctl profile preflight --arch amd64   # [NKD-E-PREFLIGHT-FAILED] target.arch hard-fail → exit 17
  ```
  Distinct per-arch profile digests (amd64 `0563f887…`, arm64 `a95f5bc6…`).
- **Profile-bounded attest → verify, with conformance claim + evidence bundle** — real from-source runc (arm64):
  ```
  nkdctl attest --role worker --k8s 1.32.0 --arch arm64 --only runc \
      --conformance k8s-conformance:v1.32 -o exp.json --evidence-out ev.json
  # manifest: recipeDigest 0aa7e1b3…  profileDigest a95f5bc6…  conformanceClaim k8s-conformance:v1.32
  nkdctl verify --role worker --k8s 1.32.0 --arch arm64 --only runc -e exp.json --evidence-out ev-v.json
  # → VERIFIED (Reproduced) … reproduced byte-for-byte UNDER PROFILE a95f5bc6…
  ```
  A verify under a *different* profile digest is a variant, not a Reproduced state. `recipeDigest` is unchanged
  by all this (`f08265d2…` control-plane amd64) — the profile/variant/conformance data is separate from the IR.
- **RAW is the canonical disk artifact.** Byte-identity attaches to the RAW logical-disk (LBA) stream
  (`assemble.LogicalDiskDigest` + a per-block manifest for diagnosable granularity); qcow2/VMDK/VHD are derived
  envelopes verified by the decoded logical-disk digest, never the container bytes.
- **Gated ValidatedVariant lane** (`verify --allow-variant`): a *pre-classified* representation cause
  (cause-code taxonomy + machine-checkable predicate published in the vector) auto-approves into a **distinct,
  non-reproduction, expiring** identity; any other cause fails closed (`NKD-E-VARIANT-REJECTED`, exit 18).
- **AICR = the accelerated-runtime declarative layer.** The `accelerator/service/intent/platform` axes activate
  for a recipe that declares a **digest-pinned AICR profile** (`github.com/NVIDIA/aicr`), carried as provenance;
  without one they still fail closed.

## Reproducible qcow2 → UEFI boot → Kubernetes API proof

`nkdctl disk --format qcow2 [--boot]` assembles a **self-contained, UEFI-bootable qcow2** from the base rootfs
+ the from-source substrate (control plane included) and can boot it under QEMU/OVMF.

**Deterministic assembly** (`scripts/_assemble-disk.sh`, run in the pinned rootfs toolchain): a GPT with
**fixed disk + partition GUIDs** (`sgdisk -U/-u`), an ext4 root with a **fixed FS UUID + hash_seed + `-O
^orphan_file`**, a FAT ESP with a **fixed volume-id** holding a `grub-mkstandalone` `BOOTX64.EFI`, all under
`SOURCE_DATE_EPOCH` with a **frozen clock (libfaketime `x0`)** so mke2fs/mkfs.fat's volatile timestamps are
constant. Four ext4 non-determinism sources were each isolated and pinned: the random htree hash-seed (`-E
hash_seed`), mke2fs's wall-clock `s_wtime`/inode times (frozen faketime), the `orphan_file` feature's
run-variable inode (`-O ^orphan_file`), and source-file atime churn between the two passes (per-pass `touch`
normalization). With these, mke2fs is **byte-identical** for an isolated file and for a 2400-file tree on the
bind mount. **Honest residual:** on the *full* Debian rootfs the two assemblies still differ in a ~969-block
region of the **inode table** — a characterized last-mile difference (something the full rootfs carries —
symlinks/hardlinks/xattrs — that the synthetic trees don't) that the isolation/synthetic reproducibility does
not yet cover. The deterministic-assembly *discipline* is complete; full-rootfs byte-identity on this
macOS-Docker host is the remaining frontier.

**Real disk boot** (not a direct-kernel shortcut): OVMF/UEFI → the on-disk **GRUB** in the ESP → kernel →
**ext4 root mounted from `/dev/vda2`** → `NKD-BOOT-OK`. The from-source control plane runs in-guest: `etcd
3.5.33`, `kube-apiserver v1.32.0`, `kubectl v1.32.0`.

**The Kubernetes API is up and creates resources** — a minimal single-node control plane from the from-source
binaries (`scripts/k8s-apiproof.sh`, run as `init`): etcd + `kube-apiserver` (bearer-token auth, non-loopback
advertise address so the `kubernetes`-service endpoint reconciler is satisfied), then the from-source `kubectl`
**creates and reads back real resources**:

```
namespace/nkd-demo created
configmap/nkd-proof created            data: {built-from: source, by: nkdctl, substrate: tier-1}
nkd-demo   Active                      metadata.uid: a160e327-…  resourceVersion: "73"
→ NKD-API-PROOF-OK
```

The objects persist in etcd (real UID + resourceVersion), served by the substrate nkdctl built. This ran
**inside the UEFI-booted qcow2** (`k8s-apiproof.sh` as `init` — `NKD-BOOT-OK` → API up → resources created →
`NKD-API-PROOF-OK`, then self-poweroff; full `.nkd/work/image/api.log`), and identically in an amd64 container
(per-process emulation, where the from-source apiserver accepts writes in ~5s). Both paths use the exact
from-source binaries — the same disk boots *and* runs a functioning Kubernetes API server.

## Multi-node cluster (Path B: containers-as-nodes, from-source binaries)

A 3-node Kubernetes cluster (1 control-plane + 2 workers) runs as containers on a Docker
network (`kind`), with nkdctl's **from-source** node stack driving it and an example app
scheduled across the workers (`scripts/cluster/`).

- **From-source binaries run the nodes.** After the cluster bootstraps, our from-source
  `kubelet`/`kubeadm`/`kubectl` (v1.32.0) are installed into every node and kubelet
  restarted; all three nodes re-report `Ready`, and the running `/usr/bin/kubelet` on each
  node has the **same sha256 as our built binary** (`3ddb7494…`, proof it is ours, not
  kind's). `scripts/cluster/swap-binaries.sh`.
- **Example app across nodes.** An `nginx` Deployment (3 replicas, topology-spread) schedules
  across **both** workers (`10.244.1.x`/`10.244.2.x`), and is reachable through a ClusterIP
  Service (pod → CoreDNS → Service → pod → `Welcome to nginx!`). `scripts/cluster/deploy-app.sh`.
### Every image is ours — seccomp fixed, control plane from-source

A second iteration made the cluster run **entirely from our from-source images** (no kind
stock images), which required fixing two real issues:

1. **Seccomp fix.** Our catalog builds containerd `CGO_ENABLED=0` *without* `-tags seccomp`,
   so it reports `seccomp is not supported` and cannot run any pod that requests the default
   seccomp profile (kubeadm's control-plane static pods do). Rebuilding containerd with
   `-tags seccomp` + libseccomp fixes it — verified directly: a pod pinned to a node running
   our containerd with `securityContext.seccompProfile: RuntimeDefault` goes `Running` with no
   error. (This is a catalog gap, noted at the containerd entry in `registry.yaml`.)
2. **Native arch.** amd64-on-arm64 emulation makes *any* amd64 containerd report seccomp
   unsupported, so the cluster runs **native arm64** and the whole stack is rebuilt for arm64
   from the pinned sources (`make KUBE_BUILD_PLATFORMS=linux/arm64`, etc.).

**Result (`scripts/cluster/{build-images,build-node-full,create-full,install-cni}.sh`):**
the control plane (`kube-apiserver`/`controller-manager`/`scheduler`/`etcd`), `kube-proxy`,
`coredns`, and `pause` all run **our** container images (built from our binaries, tagged with
the exact refs kubeadm requires, preloaded into the node's containerd) — proven by a functional
check (our images are debian-based with a shell; the real k8s images are distroless). The node
runs **our** seccomp-enabled containerd + runc + kubelet/kubeadm/kubectl, and pod networking is
**our** cni-plugins (kind's kindnet CNI and local-path storage addon are removed). A
`kubectl get pods -A` image census shows **only** `registry.k8s.io/...` = our builds; **no kind
images run in the cluster**. The only kind artifact reused is `kindest/node`'s base OS/systemd
harness (the "machine"); every Kubernetes binary and image is nkdctl's. Replacing that base OS
too (e.g. nkdctl's mmdebstrap rootfs made kubeadm-compatible) is the remaining step.

## Honest scope boundaries (documented, not hidden)

- **Self-contained UEFI disk boot is now real** (`nkdctl disk --format qcow2`): OVMF → on-disk GRUB (ESP) →
  kernel → ext4 root, with fixed GPT/partition GUIDs + FS UUID. Remaining disk work: an **ISO/PXE** variant and
  a proper installer; the direct-kernel `scripts/boot-node.sh` path is retained as the simpler rootfs smoke test.
- **Two-machine** bit-identity: only the within-machine varied-env `bake-off` is run; the ultimate gate needs
  two distinct machines.
- **Full-rootfs qcow2 byte-identity:** the deterministic-assembly discipline is complete (fixed GPT GUIDs / FS
  UUID / hash-seed / `^orphan_file` / frozen clock / atime normalization) and mke2fs is byte-identical in
  isolation and on synthetic trees, but two independent assemblies of the *full* rootfs still differ in a
  small inode-table region — the characterized last-mile frontier.
- **cosign** is wired but not executed here (no signing tested); packaging into `build`/`verify` is via
  `--package`.
- **Toolchain pins:** kubernetes/runc use *local* derived image IDs (rsync / musl+libseccomp); a portable
  catalog would push them and pin registry digests. etcd's toolchain is a resolved public `golang` digest.
