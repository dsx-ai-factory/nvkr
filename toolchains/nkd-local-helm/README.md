# Local Helm wrapper

This ARM64 developer-only image retains the existing pinned `alpine/helm` closure
and removes its `helm` entrypoint. It adds no binaries or package downloads.
NKTP source preparation can therefore run its declared repository-registration
sequence, then use Helm to validate and fetch the source-owned chart lock.

`scripts/nke-full-arm64-local.sh` builds and probes it for the `site` lane.
Docker uses a fixed build epoch; Podman uses a fixed timestamp. If the local image
identity differs from the recorded Docker candidate, the launcher freezes it
through the existing exact-predecessor catalog overlay. It never edits a lock,
publishes the image, changes a runtime entrypoint or uses a mutable tag as a pin.

The parent was probed as Helm **4.3.0**, not the selected NKE source's Make-required
4.2.4. The catalog invokes Helm directly and does not claim that NKE Make's tool
version gate passed. A complete site build and deployment are separate acceptance
requirements. This is not a portable published or qualified toolchain, nor a new
organizational license/security approval.
