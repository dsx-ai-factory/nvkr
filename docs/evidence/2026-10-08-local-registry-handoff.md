# Exact local controller/chart registry handoff, October 8, 2026

## Result and scope

The existing completed four-output site Run's controller image and bound Helm
chart were copied through source-built ORAS into a real temporary OCI registry
inside Colima. A subsequent registry descriptor fetch matched each observed
manifest digest exactly. No application, image or chart rebuild ran.

This is two-output **diagnostic transport evidence**, not an NKE installation,
complete dependency mirror, node pull/consumer acceptance, signed publication,
Release authentication or full demo. Blob contents were transported by ORAS;
the independent read-back assertion covers manifest digests, not a separately
extracted/rehashed consumer copy of every blob. The registry was loopback-only
inside Linux and is not asserted reachable from Kind or tenant nodes.

## Exact inputs and commands

Input Run: `laptop-colima-site-arm64-20261008-01` under
`/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-ae535278b8afe70e07a69ddb51c86087773ac34f/.nkd/runs`.
Its local lifecycle description was checked for `complete`, four expected and
observed outputs, and no failure. Each selected OCI index had exactly one
manifest matching the Run's observation.

ORAS 1.3.3 came from the already NKD-source-built/extracted
`installer-tools-arm64` bundle in
`laptop-colima-tools-arm64-20261008-01`. No tool release executable was downloaded
for this probe. This diagnostic reuse does not turn its local observation into
authenticated Release authority.

| Output | Source and registry read-back manifest |
| --- | --- |
| `cwe` | `sha256:ba55320fdf6efd39be43c15eedc8e60126914bd50f9eb758b984ae84bf5eb699` |
| `controller-chart` | `sha256:924b2d52b3a5717afdeb46a8341ac537f831b3250e4090b751676ffaba6d7ec6` |

The exercised transport for each selected output was:

```sh
timeout 180 "$NKD_ORAS" cp --from-oci-layout \
  "$NKD_SITE_RUN/packages/$nkd_output@$nkd_digest" \
  "127.0.0.1:15500/nkd-probe/$nkd_output:run01" --to-plain-http
timeout 30 "$NKD_ORAS" manifest fetch --plain-http --descriptor \
  "127.0.0.1:15500/nkd-probe/$nkd_output:run01"
```

These are the probe's selected variables, not a complete independently
copy-pastable deployment command. See the [handoff guide](../nkd-to-nke-demo.md)
for prerequisites and the still-unimplemented complete adapter.

## Service ownership and cleanup

The repository's M1 demo already uses the `registry:3` operator service. This
probe acquired that service, resolved its current bytes and ran
`docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8`.
It is an acquired diagnostic host service, not a source-built NKE output or an
organizationally approved toolchain closure. The runtime image digest matched.

Container name: `nkd-demo-handoff-registry-20261008`;
ownership label: `dev.nkd.demo.owner=pr118-registry-probe-20261008`;
port binding: `127.0.0.1:15500` to container 5000. An existing name was refused.
The captured exact container ID, verified owner label and EXIT/INT/TERM cleanup
owned its stop. `--rm` removed its temporary container-owned data after stop; this
discarded only that probe's copied blobs. Accepted Run outputs were untouched.

The command exited 0. Read-back found no named container or listener at 15500;
Docker remained 29.5.2/native ARM64. The pulled operator service image remains
cached. No public/private remote registry was written, no default Mac Docker
context changed and no shared cluster was adopted.
