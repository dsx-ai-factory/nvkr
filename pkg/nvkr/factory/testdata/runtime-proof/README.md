# Bounded runtime guest

This fixture boots a generated x86 BIOS sector, writes that sector to LBA 8,
and shuts down through the QEMU pc machine's ACPI port. Packer builds an 8 MiB
QCOW2. Its post-processor checks the guest write, consumer UID and capabilities,
and an exact snapshot metadata download through the produced outbound helper.

Regenerate `seed.raw` with `python3 generate-seed.py`. The generator contains
the complete guest instruction bytes and disk-address packet. It downloads
nothing. The guest uses the runtime's already-pinned SeaBIOS package.

The opt-in factory test owns the original runtime production, installed-package
SBOM and completed consumer receipt until the held disk is exported. It does not
exercise hosted provider admission, a complete public Run, signing, publication,
the maintained NVKE guest, libguestfs inspection, EFI or arm64. Exact old-image
comparison remains a separate hosted development Run gate.
