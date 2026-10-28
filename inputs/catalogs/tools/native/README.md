# Shared native NVKE tool catalog

This directory owns one source registry and machine-managed lock for both native demo hosts. The Mac and Linux Definitions select platform-specific outputs from this same catalog. Deployment orchestration remains shared Go code.

The retired standalone simulator catalog is consolidated here: Linux and Mac demo selections use the same producer and source lock. The SQLite server requires CGO. Linux uses its static source build; local Mac preparation binds the installed Go SDK and Apple Clang through NVKR before the same tool Run. The demo dependency owner invokes that preparation automatically on Mac. It downloads no compiler, and the native Go execution lane cannot publish or sign. The API-only simulator CLI remains CGO-free. The source-owned installer archive and Linux site runtime are shared. Source producers consume their pinned upstream code and module files unchanged; dependency preparation verifies those module files before and after vendoring.

Upstream acquired executables have distinct Linux producer IDs (`clusterctl-linux`, `helm-linux`, `kind-linux`, `kubectl-linux`, `oras-linux`) because each asset has one exact platform and digest. After NVKR verifies and extracts the selected bundle, one downstream adapter creates a private view with conventional command names. It retains the original extraction and verifies every copied file against its bundle entry. Immutable Run outputs are never renamed or rewritten. Mac-only vmnet producers are not selected by Linux outputs.

The installer archive includes NVKE’s shipped site-spec template. The native adapter reads its Kubernetes default from the verified installer payload and rechecks the original bytes before site startup and tenant operations; the version is not a CLI constant. Site packages, tenant requests, node readiness and the conformance configuration use that same selection.

Native bootstrap and tenant requests use the selected unmodified NVKE interfaces. The Mac site adds a real QEMU software TPM through NICoSim’s supported executable override, using installed QEMU, swtpm and libtpms. Linux retains libvirt’s TPM device. Catalog validation and bounded device/network probes do not establish guest attestation or full native runtime acceptance.

The selected Darwin NICoSim requires the genuine `vmnet-client` command and its `--unprivileged` option.
The vmnet pair therefore uses upstream v0.10.0: [v0.11.0](https://github.com/nirs/vmnet-helper/releases/tag/v0.11.0)
renamed that command to `vmnet-run` and removed the option. Private extraction does not satisfy NICoSim's
installed-path lookup. It searches `/opt/vmnet-helper/bin/vmnet-client` and Homebrew's `libexec` locations;
the upstream client itself requires `/opt/vmnet-helper/bin/vmnet-helper`. This unprivileged route requires
macOS 26 or later. Host installation and a successful guest-network test remain separate prerequisites.
Before starting the bridge or NICoSim, the demo compares that first installed client and its helper
against the selected verified tool-bundle bytes. A missing, unsafe or mismatched installed pair returns
a capability error; it does not fall back to a later installation or install host tools.

Use the existing CLI to change pins. QEMU, firmware, ISO utilities and host privileges are prerequisites; these tool bundles do not install them. See the [operational guide](../../../../docs/local-nvke-demo.md#native-linux-and-mac-path) and its runtime evidence limits.

`nvkr-site-identity` is an exact commit self-pin to this repository. `catalog bump`
rejects commit-mode pins; update it explicitly with `catalog pin --commit` after
the helper changes. Preserve the pinned commit when updating a PR branch. After a
squash merge, re-pin it to that commit on `main` in a follow-up change so the helper
has canonical-main provenance. A PR-branch self-pin does not establish that provenance.

Packer 1.15.4 and its QEMU plugin 1.1.6 have separate Darwin and Linux ARM64 acquisitions in this same catalog, using the versions already selected by the Packer toolchain lock. Their ZIP carriers and selected executable bytes have independent CLI-managed pins. These acquisitions do not by themselves admit native Mac image execution or establish a successful QCOW build. They do not install tools globally or publish artifacts.

The same pinned `nerdctl` source producer supports Darwin ARM64 and Linux ARM64. Both host tool bundles
select it under its conventional command name; the site-startup bundle separately selects Linux guest bytes.
The native CLI requires a containerd endpoint. Both hosts use an owned private Lima backend through
its upstream `nerdctl.lima` entry point for temporary bootstrap; NVKE and Kind orchestration remain on the host.
The Linux Lima candidate remains restricted to the explicitly approved private diagnostic pilot; its outstanding
binary-scan finding is not cleared by tool compilation or demo checks. Installing the CLI does not create a
containerd daemon or make Docker's API a containerd endpoint. Tool acquisition, native compilation and daemon
readiness remain separate from complete demo acceptance.
