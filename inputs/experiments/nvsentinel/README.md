# NVSentinel package in NVKE OS inputs

This experiment keeps separate Debian producer copies with byte-identical commands and matching source
declarations and pins from the [dedicated NVSentinel catalog](../../catalogs/nvsentinel/README.md).
The offline catalog regression checks both copies and their callers together. Its private archive assembly adds
the raw package to the existing `debs/` folder, regenerates the APT index and passes it to unchanged Packer
code. Placement makes the package available in the guest local APT repository; it does not install or enable it.

`catalog/` retains the pinned NVKE consumer and its material suppliers. `variants/` contains the four
predecessor-bound Ubuntu/platform overlays. The adjacent `os-*.yaml` Definitions use local execution
policies under `../../environments/nvsentinel/`. The catalog guide owns commands, source revisions and
validation limits. These experimental inputs do not modify the maintained NVKE release.
