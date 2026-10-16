# Acquired native Mac tools

This focused Definition acquires the selected Darwin ARM64 `clusterctl`, Helm, kubectl, ORAS, Packer, its QEMU plugin and vmnet executables. It references the [shared native catalog](../../../catalogs/tools/native/README.md); there is no duplicate registry or lock.

For the complete source-built host bundle and Linux site companions, use `nvke-demo tools` with the [native Mac selection](../native-mac/README.md). Output verification and private extraction remain owned by NVKR. No system installation or sudoers changes occur. QEMU, firmware and ISO tooling remain separate prerequisites.
