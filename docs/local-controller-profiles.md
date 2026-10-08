# Build the local controller profiles image

The separate `profiles` lane builds one small Linux ARM64 OCI image with the
configuration tree NKE's controller chart materializes under `/profiles`.
It does not rebuild the OS, start NICoSim, install NKE or create a tenant.
Start with [Docker/Colima setup](local-development.md#start-from-a-clean-machine).

```sh
# Apple Silicon: explicitly select your already running ARM64 Colima profile.
CONTAINER_RUNTIME=docker COLIMA_PROFILE=nkd-demo \
  bash scripts/nke-full-arm64-local.sh --check profiles
CONTAINER_RUNTIME=docker COLIMA_PROFILE=nkd-demo \
  NKE_LOCAL_CLONE=/absolute/path/to/nke \
  bash scripts/nke-full-arm64-local.sh first-profiles 1 profiles
```

Use a fresh attempt name. Omit `NKE_LOCAL_CLONE` for canonical remote Git;
private-source access must then work inside the VM. Native Linux ARM64 runs the
same commands without `COLIMA_PROFILE`. Podman uses `CONTAINER_RUNTIME=podman`.
Docker and Podman have compiler/preflight contracts. No private profiles-build
acceptance report is shipped here; check your own completed Run and image content.

The [Definition](../definitions/nke-laptop-arm64/definition-profiles.json)
selects `controller-profiles` from its own
[catalog](../definitions/nke-laptop-arm64/profiles-catalog/registry.yaml).
Its [machine-managed lock](../definitions/nke-laptop-arm64/profiles-catalog/pins.lock.yaml)
selects the same NKE `8fb2d53368823bd386b4fbd55b4ab6d3c0931345` candidate as
the four-output `site` lane. This is not an approved release pin.

## Contents and differences from the source fixture

| Input | Produced content |
| --- | --- |
| NKE fixture Dockerfile | Same copy/materialization layout; base changed from its `alpine:3.24.1` tag to the already maintained exact Alpine digest, observed as Alpine 3.24.2 |
| ARM64 VM cloud-init fixture | SHA-guarded source template projected without line 98's HTTP agent override. First-boot package refresh is disabled; APT installs select only the baked local repository and explicitly exit on failure. An idempotent boot command adds `net.ifnames=1` through an exact `zz-nkd-local-network.cfg` snippet after the installer's `99-console.cfg` override, preserving the physical uplink name that OVN records on first boot. An existing different snippet or symlink fails |
| NKE VM config profiles | Source `nico-vms-cp` and `nico-vms-worker`, unchanged |
| NKE fixture runbooks | All nine source runbooks, unchanged |
| Local OS metadata | One `nke-local-26-04-arm64-plain` entry: ARM64, Ubuntu 26.04, injected Kubernetes version, diagnostic `local-plain-tpm` variant |
| Local boot script | ARM64 console, local QCOW2 URL, no LUKS key or FDE claim |

The producer prepares a private context only in its declared `bin` write
directory. It checks both upstream file hashes before the mechanical edits;
source drift fails instead of editing an arbitrary line. The fixture files in
the NKE checkout and the accepted source closure remain unchanged. BuildKit
resolves the pinned image base; Dockerfile build steps use network mode `none`.
The enclosing wrapped producer still has declared build networking and is not
a qualified or completely hermetic closure.

Cloud-init still installs source-declared NKE/Kubernetes/CNI packages using APT,
including the unversioned `nke-agent` command. The laptop projection selects only
`/etc/apt/sources.list.d/nke-local.list`, whose `file:/usr/lib/nke/debs` package
bytes and indexes are prepared during the bake. Missing packages terminate the
command sequence before service restarts and the completion marker; they do not
authorize public-repository downloads. This does not prevent separate runtime
container-image acquisitions or prove complete offline deployment. Removing the HTTP override does
not guarantee preservation of the held disk's installed agent bytes: the
downstream runtime must verify the selected packages and resulting executable.
Its Calico and OVN branches select 3.30.1 and 1.4.0 respectively. Successful
rendering does not establish that either package set is available, compatible or
boots successfully with every selected disk. TPM attestation is not proved by
having the plugin path in a template. The retained disk's missing DSX material
and failed complete OS smoke remain separate limitations.

## NKE handoff, not an NKD deployment feature

NKE's chart runs the profiles image as an init container and copies `/profiles`
into its shared profiles volume. The downstream installer takes the resulting
image reference in `images.profiles`. Use the accepted manifest digest and a
registry reachable by the management cluster; don't rebuild or edit the produced
image during deployment. See the [NKE handoff guide](nkd-to-nke-demo.md).

The authored local QCOW2 location is:

```text
http://172.30.0.1:18080/nke-node-image-arm64.qcow2
```

This is a diagnostic endpoint description, not a content digest or an
authenticated disk receipt. The new [deploy-only adapter](local-nke-demo.md) must
serve the exact held Run output there, read back/recheck its SHA-256 and size,
and establish actual NICoSim/controller/node reachability; complete runtime
validation remains in progress. It must reject an
occupied/foreign endpoint and incompatible disk selection. The EFI URL is a
NICoSim parser placeholder, not a supplied bare-metal EFI artifact; this image
does not enable physical-node provisioning.
Port 18080 is reserved here for the disk HTTP service, not NICoSim's API. The
existing guest probe uses API port 18089. Do not start a simulator with its
default API port 18080 and assume that it also serves this disk URL; the complete
adapter must explicitly choose distinct ports and own both services.

## Validation scope

The separate selection requires one observed output. The diagnostic content test
uses NKE's actual metadata loader and template helpers to validate its profile
selection and render both CNI branches as YAML. This is neither a complete OS
smoke nor deployment acceptance; no private Run acceptance report is shipped here.

The diagnostic test source is
[`contract_test.go`](../scripts/testdata/nke-local-profile/contract_test.go).
It belongs in a disposable copy of the pinned NKE `controller/internal/utils`
package, with `NKD_PROFILES_DIR` pointing to content extracted from the produced
image. Never add it to an accepted Run's source closure or your NKE checkout.
The diagnostic command is `go test ./internal/utils -run TestNKDLocalProfile
-count=1 -v` with the source's existing module pins. It is not a new YAML/iPXE
parser and does not validate the source's entire provisioning protocol.

`profiles` is separate from `all`, `components` and `site`. Their membership is
unchanged; separate completed Runs do not become one aggregate success.
