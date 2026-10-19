# FDE bootstrap and key-material audit

This historical source audit covers NVKE OS commit `ff21ba8c07cafff74b68e238a8876af11e5beded`,
selected by M2 before its `v0.22.0-beta.1` update. The separate SPIRE material retained its older pin.
The findings below describe that inspected tree and do not establish an audit of the current OS pin.
It supports using the documented public `changeit` bootstrap value for a diagnostic build.
That value has no confidentiality requirement, but it remains an effective disk-unlock credential.
The diagnostic output must not imply production confidentiality or successful TPM enrollment.
This audit inspected source and reproduced the GRUB substitution locally; it did not build,
boot, mount, or inspect an FDE guest.

## Build inputs and retained material

The [FDE Packer build](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/nke-node-fde.pkr.hcl)
selects `nke-node-fde.qemu.nke-fde` and produces `fde/nke-node.qcow2` using the same installer
and image-input bindings as the plain image. It configures no TPM device. TPM enrollment is
deferred to the target's first boot, so the bake needs no `swtpm`; the separate
[FDE demo](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/demo-fde.sh)
uses `swtpm` to test enrollment and subsequent boots.

The [bootstrap variable](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/variables.pkr.hcl#L192)
defaults to `changeit`. The source transports and retains it as follows:

| Location | Source behavior |
|---|---|
| Installer HTTP user-data | Contains the LUKS storage password and late commands with that value. |
| `/etc/luks-build.key` and build initramfs | Contains the same passphrase bytes, not an independent random key. Used for the unattended installer reboot. |
| LUKS keyslot | Remains an unlock method after baking; firstboot attempts to remove it after verifying TPM unseal. |
| `/etc/default/grub.d/99-luks-key.cfg`, rendered GRUB and kernel command line | Carry `luks.key=<passphrase>` for unattended bootstrap. |
| `/etc/luks-tpm-firstboot.env` | Stores `LUKS_PASSPHRASE` with mode 0600 for the firstboot service; deleted on its success path. |

The [autoinstall template](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/cidata/autoinstall-lvm.pkrtpl#L131)
creates the build keyfile and GRUB input.
[FDE configuration](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/scripts/configure-fde.sh#L48)
attempts keyslot removal, deletes the keyfile, disables automatic keyfile inclusion, regenerates
initramfs, and writes the firstboot environment file. Keyslot-removal errors are suppressed there;
source intent does not prove that an emitted disk contains only the intended slots or no old key copies.

The attempt's SSH **client private key** stays with the Packer caller; only its public key is injected
into the guest. [Guest provisioning](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/scripts/provision.sh#L129)
removes that authorized public key and runs best-effort cloud-init cleanup. Guest SSH **host private
keys** are different material: the source does not explicitly remove them. Nor does it explicitly
verify removal of installer logs or old initramfs backups. Their final presence needs live disk inspection;
this audit does not establish an image-wide absence of private key material.

APT acquisition capture is available as `fde/apt-acquisitions.tar`. The
[capture steps](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/nke-node-fde.pkr.hcl#L122)
run after FDE package installation and before final cleanup. The
[exporter](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/packer/scripts/capture-apt-acquisitions.py#L57)
collects bounded Debian archives and metadata, rejects repository URIs containing credentials,
and does not serialize the environment or FDE files.

## Public bootstrap policy and secret transport

Treating exactly `changeit` as an explicitly public diagnostic input avoids creating a secret channel.
It is acceptable to expose this value in the Definition and diagnostic output under that policy.
The existing prohibition on transporting real secrets through public variables remains necessary.

A GitHub Environment secret would protect storage and access before execution, but this pinned source
would still embed its value in the image and installer payload. Kernel command-line values may also
appear in serial and installer logs. Packer's `sensitive = true` cannot remove those copies.
There is no generic secret-valued FDE environment channel in this diagnostic route. Supporting one
requires a source contract and retention behavior that protect actual secrets throughout their lifetime.

## Firstboot limits

The [firstboot script](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/files/luks/luks-tpm-firstboot.sh#L241)
binds the existing LUKS volume key through Clevis, verifies unseal, then attempts passphrase-slot
removal. It does not re-encrypt the volume with a new key. **Inference:** clones of this QCOW2 retain
the same volume key, so access to the public-bootstrap original permits recovery of a key that can
decrypt later data on its clones, including after TPM enrollment. The cryptsetup maintainer's
[FAQ, item 6.15](https://github.com/mbroz/cryptsetup/blob/main/FAQ.md)
documents that cloning preserves the volume key and changing passphrases does not rotate it.
A node-specific volume key requires a separate initialization or re-encryption design; TPM keyslot
enrollment alone does not establish confidentiality for deployed clones.

The [GRUB cleanup substitution](https://github.com/dsx-ai-factory/nvke/blob/ff21ba8c07cafff74b68e238a8876af11e5beded/os-image/files/luks/luks-tpm-firstboot.sh#L323)
also misses the pinned template's value: its pattern requires a space before `luks.key`, while the
template puts the key immediately after the opening quote. This exact local reproduction returns
the input unchanged:

```sh
printf '%s\n' 'GRUB_CMDLINE_LINUX_DEFAULT="luks.key=changeit"' |
  sed 's/ luks.key=[^ "]*//g'
# GRUB_CMDLINE_LINUX_DEFAULT="luks.key=changeit"
```

Successful removal of the corresponding LUKS slot can make the retained string unusable for normal
unlock, but it does not prove string cleanup or resolve the cloned-volume-key issue. These source
limitations do not prevent a diagnostic bake; they prevent treating that bake as deployment-security
acceptance.
