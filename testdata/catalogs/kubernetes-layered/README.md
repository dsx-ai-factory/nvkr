# Layered Kubernetes contract fixture

This catalog exercises layered Recipe composition, catalog stacks, deterministic identities, and source-pin
maintenance in tests. It is explicit test data and is not a shipped product catalog or supported build path.

The fixture retains enough components to test role filtering and multi-source graphs. The maintained NVKE
Definition selects its product producers from `inputs/catalogs/`; NVKE OSbuild owns the worker payload.
