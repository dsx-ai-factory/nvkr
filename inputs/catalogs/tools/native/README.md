# Shared native NVKE tool catalog

This directory owns one source registry and machine-managed lock for both native demo hosts. The Mac and Linux Definitions select platform-specific outputs from this same catalog. Deployment orchestration remains shared Go code.

The retired standalone simulator catalog is consolidated here: Linux and Mac demo selections use the same producer and source lock. The SQLite server requires CGO. Linux uses its static source build; local Mac preparation binds the installed Go SDK and Apple Clang through NVKR before the same tool Run. The demo dependency owner invokes that preparation automatically on Mac. It downloads no compiler, and the native Go execution lane cannot publish or sign. The API-only simulator CLI remains CGO-free. The source-owned installer archive and Linux site runtime are shared. Source producers consume their pinned upstream code and module files unchanged; dependency preparation verifies those module files before and after vendoring.

Upstream acquired executables have distinct Linux producer IDs (`clusterctl-linux`, `helm-linux`, `kind-linux`, `kubectl-linux`, `oras-linux`) because each asset has one exact platform and digest. After NVKR verifies and extracts the selected bundle, one downstream adapter creates a private view with conventional command names. It retains the original extraction and verifies every copied file against its bundle entry. Immutable Run outputs are never renamed or rewritten. Mac-only vmnet producers are not selected by Linux outputs.

Native bootstrap, networking, TPM and database portability must be provided by the selected NVKE commit. Native runtime acceptance must be re-established against that unmodified source; catalog validation does not establish it.

Use the existing CLI to change pins. QEMU, firmware, ISO utilities and host privileges are prerequisites; these tool bundles do not install them. See the [operational guide](../../../../docs/local-nvke-demo.md#native-linux-and-mac-path) and its runtime evidence limits.

Packer 1.15.4 and its QEMU plugin 1.1.6 have separate Darwin and Linux ARM64 acquisitions in this same catalog, using the versions already selected by the Packer toolchain lock. Their ZIP carriers and selected executable bytes have independent CLI-managed pins. These acquisitions do not by themselves admit native Mac image execution or establish a successful QCOW build. They do not install tools globally or publish artifacts.

The same pinned `nerdctl` source producer supports Darwin ARM64 and Linux ARM64. Both host tool bundles
select it under its conventional command name; the site-startup bundle separately selects Linux guest bytes.
The native CLI requires a containerd endpoint. Mac temporary bootstrap uses an owned Lima backend through
its upstream `nerdctl.lima` entry point; NVKE and Kind orchestration remain on the host. Linux uses its native
`nerdctl`. Installing the CLI does not create a containerd daemon or make Docker's API a containerd endpoint.
Tool acquisition, native compilation and daemon readiness remain separate from complete demo acceptance.
