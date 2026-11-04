## Ubuntu 24 node-image inputs

The opt-in `inputs/definitions/diagnostics/ubuntu24` selections reuse M2's NVKE source, now
`v0.23.0` at `2cfd00081b468d85a4894ec17e9deac13c9d8169`, including upstream Ubuntu 24 support from
`96ccc50cb7f8ed8bb08f7cf8655906982774f809`. A predecessor-bound whole-producer overlay
selects Ubuntu 24.04.5 installer bytes for each native architecture, the 24.04 DOCA OVS packages and a
separate Go 1.27.3 Bookworm agent builder. The authenticated installer material selects the guest
OS; no new Packer variable or NVKR platform axis is introduced. Preparation checks both
nFPM archive and executable pins; the agent stays dynamically linked and must pass the
pinned upstream ELF checker for the Ubuntu loader and GLIBC <= 2.39. Its updater must
remain a static ELF without a program interpreter, matching the pinned upstream check.
Other M2 materials and the Packer host runtime retain their existing scope and exceptions.

Single-output hosted Definitions cover plain and public-bootstrap FDE on AMD64 and ARM64;
the existing native local wrapper also selects the two plain variants. Hosted build-only
finalization retains its one-disk limit. The supplied Environments generate raw-disk
`static-unqualified` Runs without signing or publication.
`pkg/nvkr/lifecycle/nvke_ubuntu24_test.go` covers compilation, exact closure/pins, changed
Build identity, native KVM admission and deterministic generation. Script fixtures cover
selection and frozen-input routing through the shared release catalog. Disk producers
retain stock NVKE capture source; the material owner accepts its original acquisition schema. The input-layout migration preserves the agent preparation
and build recipe used in the package exercise below. This is contract evidence: no Ubuntu 24 KVM bake,
guest boot, full acquired-payload ABI inspection, agent-start smoke, release publication
or qualification is established by these tests. ADR-10/11/18/22 own the unchanged contracts.

Maintained publication uses the sibling `inputs/definitions/nvke/disks-ubuntu24.yaml` family. It selects
`nvke-node-image-ubuntu24-<arch>` and `nvke-node-fde-ubuntu24-<arch>` for AMD64 and ARM64 through the same
overlay stack, with its own `github-nvke-disks-ubuntu24` Environment, main-only
`nvkr-run-nvke-disks-ubuntu24.yml` signer and `m2-candidate/disks-ubuntu24` prefix. Private toolchain
preparation admits that exact prefix. The combined M2 candidate, its Ubuntu 26.04 disks and the `disks`
family are unchanged; ADR-26 refuses both series as separate composite members. Split, composite, closure
and credential-boundary contracts pin each output's exact overlay stack and closure, signer isolation and
credential projection; generation is deterministic. No Ubuntu 24.04 hosted bake, publication, release
verification or guest boot is recorded.

On 2026-10-19 (UTC), a local native ARM64 container exercise ran the then-current overlay preparation
and offline build commands against the earlier `ff21ba8c07cafff74b68e238a8876af11e5beded` source with the declared Bookworm image
`sha256:2bcff0c6ad6e8c5353e8c0d5c7f17bfcc87d667d0169615e34db1acc0db985b9`.
Module verification, both nFPM checksum checks, packaging and the ELF guards passed.
The resulting `nke-agent` Debian package, version `0.2.1-81-gff21ba8`, had SHA-256
`5f4267a3af6280db68955e9a3c3cce7ccf67a6af2054517bbfb58a0bad37a132`.
The dynamic agent required at most `GLIBC_2.34`, used `/lib/ld-linux-aarch64.so.1`
and depended only on `libc.so.6`; the updater had no interpreter or dynamic section.
Both extracted package binaries matched the build outputs. This is direct recipe evidence,
not an authenticated NVKR Run, an AMD64 package exercise, or disk/guest acceptance.
