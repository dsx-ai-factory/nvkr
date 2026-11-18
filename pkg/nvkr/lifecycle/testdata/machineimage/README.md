# Pinned MachineImage schema

`machineimages-crd.yaml` is an unchanged copy of the NVKE CRD at commit
`28b9e338c1d08a4a0427b7c6d79c8174358ad991`:
https://github.com/dsx-ai-factory/nvke/blob/28b9e338c1d08a4a0427b7c6d79c8174358ad991/controller/deploy/charts/nvke-crds/bases/internal.nvke.nvidia.com_machineimages.yaml

The upstream Git blob is `4bee5ee5fb51249d2839509e1e8cafda83bb3c54`; the local fixture
was checked against that exact blob, including its SPDX header.

The local test checks required fields, string types, bounds and patterns from this fixture.
Script template/header rules are asserted separately; this is not Kubernetes API-server/CEL
admission, an admitted imager profile, signed-artifact verification or boot evidence.
Refresh from an explicitly reviewed source pin; do not edit this generated CRD in place.
