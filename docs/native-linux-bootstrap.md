# Native Linux bootstrap backend

The Linux adapter reuses the Mac demo's private temporary Lima owner.
NVKR, `nvkectl` and Kind execute on the Ubuntu host; container commands use
upstream `nerdctl.lima` to reach the private backend. The permanent site and
tenant guests remain NICo-managed QEMU/KVM VMs. The backend must stop and join
before tenant provisioning. The source implementation has ownership and version
admission tests. One explicitly approved private backend pilot passed; the complete
fresh site/tenant demo remains required.

## Selected tool and source

The Linux diagnostic candidate is the unmodified upstream Lima commit
[`f47461ed7d03f300297e01fd659e9aefc6c228b9`](https://github.com/lima-vm/lima/tree/f47461ed7d03f300297e01fd659e9aefc6c228b9),
tree `525cd5017b050291ab824892d624bbbacf7f8e7e`. It is a development revision,
not the stable 2.2.1 release. Its selected local build reports the honest
source-derived version `f47461e`; no release tag or module version is fabricated.
Mac's existing 2.2.1 backend is a separate platform prerequisite. These backend
versions do not change the shared NVKE, CAPNICo or NVIDIA OVN source selection.

The candidate was built with Go 1.27.2 using upstream `make binaries`, retaining
matching helpers, Linux guest agents and templates. Generated manpages and a
release archive were not built. The selected ARM64 limactl is SHA256
`3c9f2b85164973a53a08e5bb1beb9a10eca27a8723cab6db2c802d46334370a3`.
This is a local diagnostic build record, not an upstream release signature.

## Install from public source on Ubuntu ARM64

Use the Go version in the NVKR checkout's `.go-version`, with Go, Make, GCC and
Git on `PATH`. The Ubuntu prerequisite packages in the
[demo guide](local-nvke-demo.md#2-install-prerequisites-on-the-ubuntu-execution-host)
include the compiler. Run this once in your normal user account. The source and
installation directories must be fresh; do not replace an existing Lima install.

```sh
umask 022
lima_source="$HOME/.local/src/lima-f47461e"
lima_prefix="$HOME/.local/opt/lima-f47461e"
test ! -e "$lima_source" && test ! -e "$lima_prefix" &&
mkdir -p "$HOME/.local/src" "$HOME/.local/opt" &&
git init "$lima_source" &&
git -C "$lima_source" remote add origin https://github.com/lima-vm/lima.git &&
git -C "$lima_source" fetch --depth=1 origin f47461ed7d03f300297e01fd659e9aefc6c228b9 &&
git -C "$lima_source" checkout --detach FETCH_HEAD &&
GOTOOLCHAIN=local make -C "$lima_source" native documentation install PREFIX="$lima_prefix" &&
install -m 0644 "$lima_source/NOTICE" "$lima_prefix/share/doc/lima/NOTICE" &&
export PATH="$lima_prefix/bin:$PATH" &&
limactl --version &&
command -v nerdctl.lima
```

The version must report `f47461e`. These commands use upstream's native build
and installation targets to install the matching host tools, ARM64 guest agent
and templates together. They require no previously prepared NVKR tool bundle.
Keep the exported `PATH` in each terminal used for the demo. The CLI reports a
missing or incompatible prerequisite before its E2E build; it does not install Lima.

Building with the current NVKR Go SDK can produce different binary bytes from
the historical Go 1.27.2 pilot above. Its recorded hash and runtime observations
do not authenticate or establish acceptance of a new build. The fresh full demo
must exercise the newly installed prerequisite and record its result.

## Vulnerability evidence and its scope

The required NVKR source vulnerability gate remains unchanged; see
[vulnerability management](cve-management.md). The procedure separately requires
reachability and exposure analysis for upstream artifacts. It does not specify
a universal zero-advisory binary-mode gate for every external executable.

Actual source checks of the exact candidate with pinned govulncheck 1.9.0 and
Go 1.27.2 passed on Linux ARM64 with both CGO enabled and disabled. These match
the host/helper and native guest-agent build configurations. Both checks reported
zero affected imported packages and zero called vulnerabilities; DHCP
[GO-2026-6237](https://pkg.go.dev/vuln/GO-2026-6237) and OpenPGP
[GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) remained required-module-only
findings. This does not make the old module revisions fixed or establish every
possible dynamic execution path as safe.

The exact stripped limactl binary scan returned nonzero and remains preserved.
Its ELF static symbol table is absent. The pinned scanner therefore uses
[module-level fallback](https://github.com/golang/vuln/blob/v1.9.0/internal/vulncheck/binary.go#L107),
populating affected symbols from advisory data rather than a binary call graph.
Its DHCP/OpenPGP labels are reconciled with the matching source checks at that
documented precision. They are not suppressed, and the binary scan is not called
clean.

The candidate also contains the main-line
[guest-agent socket correction](https://github.com/lima-vm/lima/commit/b08cae8a670cf916d5da11c48a6de76dabd89678)
for [GO-2026-6230](https://pkg.go.dev/vuln/GO-2026-6230), including the selected
guest agent's socket-owner and mode-0600 behavior. A shallow-build pseudo-version
can place that fixed source in the advisory's older version range. Preserve the
actual source and version metadata; never stamp a false stable release to change
the report. This evidence does not clear the separately failed official 2.2.1
binary or source checks.

## Runtime materials and private distribution

The proposed QEMU backend selects one image and one containerd archive from the
same upstream source, replacing inherited image lists so a digestless fallback
cannot be used:

| Material | Exact source selection |
| --- | --- |
| Ubuntu ARM64 server image | `https://cloud-images.ubuntu.com/releases/resolute/release-20260927/ubuntu-26.04-server-cloudimg-arm64.img`, SHA256 `63a93bd5a8d76e33b15ceb5daa3657bd79be804748051ab178e643b0f5da22e7` |
| nerdctl-full 2.4.1 ARM64 | `https://github.com/containerd/nerdctl/releases/download/v2.4.1/nerdctl-full-2.4.1-linux-arm64.tar.gz`, SHA256 `2deffe14625f93866f90a8f265d0f59d6fa476c7dc54b98adb327b1b4613efd4` |

These are acquisition pins, not completed downloads or security clearance for
every component inside an image/archive. QEMU, firmware and native libraries
remain admitted host prerequisites. Require actual usable KVM; do not fall back
to TCG. The Linux transport uses QEMU/9p and host-originated SLIRP, with the exact
owned gateway for SPIRE port 30081. The unauthenticated controller API remains
loopback-bound. No Docker subnet supplies native-host forwarding authority.

Lima is a separate Apache-2.0 executable, not a Go dependency linked into NVKR.
The [contribution policy](../CONTRIBUTING.md) distinguishes that boundary from
dependencies linked into NVKR. Its external MPL-2.0 HCL dependency therefore
does not itself alter NVKR's linked-library license closure. This distinction
does not waive the external bundle's own terms or authorize public redistribution.
Retain the exact source LICENSE, NOTICE and corresponding dependency license
materials. Upstream `make binaries` omitted its root NOTICE; the private candidate
now includes that exact 963-byte text at `share/doc/lima/NOTICE`, SHA256
`a7e0caed1395c49dfa6ce7dc8aa6362e4daa8369917f56fbe80084c1ae8c765c`.
The original 167 runtime members remain unchanged; the updated manifest has 168.

## Before a fresh demo

Admit the reviewed matched tool tree and exact source-derived version before
the artifact build. Use a fresh private Lima home and restricted mounts. Verify
the actual KVM, image/archive integrity, restricted sharing, loopback forwarding
and backend stop/join with a bounded pilot. No host daemon,
socket permission, user group or unrelated VM may be adopted or changed.

Only after those prerequisites and repository checks pass should a new signed
NVKR head be handed to both hosts for fresh artifact and tool Runs. Earlier
builds, probes and failed invocations do not establish that new head's complete
site, tenant, conformance or cleanup result.

The backend-only pilot uses Linux loopback for port 30081 and an attempt-specific
HTTP fixture. It creates no site/tenant VM or native bridge. The subsequent full
demo must separately verify actual image imports, Kind-to-site ports 15005/6443
and site-to-bootstrap SPIRE forwarding through the owned native gateway. Those
paths cannot be accepted from a loopback fixture or a versioned journal alone.

The October 18 private pilot exited naturally with status 0. Actual QEMU used
`virt,accel=kvm`; the rendered configuration retained exactly the selected image
and archive. The test checked nerdctl 2.4.1, read-only image sharing, writable
staging, an attempt-specific HTTP response through loopback port 30081, and
joined HTTP/backend shutdown. The owned VM, state directory and sockets were
absent afterward. Its test body took 125 seconds. These observations apply to
the diagnostic source snapshot and exact candidate bytes above; no full NVKE
deployment, conformance result or security exception follows from them.
