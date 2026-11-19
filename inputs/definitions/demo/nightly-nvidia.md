# Retired ARM64 NVIDIA OVN nightly producer

The signed ARM64 NVIDIA OVN producer, its Definition and generated workflow are retired.
The old build/publish/consume coordinator had already been replaced by the
[AMD64 local E2E nightly](../../../docs/nvke-nightly.md). That nightly does not establish ARM64 coverage.

Use the maintained [NVKE composite](../nvke/README.md) for signed product publication, or the retained
local TCG diagnostic inputs for platform-specific development. The composite is not a byte-equivalent
replacement for the former manual ARM64 diagnostic selection. Historical Runs retain their original scope.
