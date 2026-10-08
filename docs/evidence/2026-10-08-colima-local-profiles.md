# Local controller profiles image, October 8, 2026

## Result

The new diagnostic profiles-only NKD Docker Run completed one required/observed
OCI output with no failure. The real image ran offline and passed content
assertions. A separate disposable source copy ran NKE's own metadata loader and
template helpers against extracted image content: ARM64/Ubuntu selection and
both Calico/OVN cloud-init rendering passed.

This is a build/content diagnostic, not a site installation, QCOW2/profile boot
compatibility result, attestation, full OS smoke, tenant/workload, signed Release
or reproducibility qualification. The [guest probe](2026-10-08-native-nicosim-qcow2-guest.md)
used a different minimal cloud-init configuration; it does not validate this
new controller profile.

## Identities and execution

Implementation: base PR #118 commit
`33a4243795a14fd49f03e826b645f12baa02de16` plus the new authored profiles inputs
and launcher change. These were dirty candidate files at execution time, not a
committed-head or CI claim. The initial Run01 completed and passed the image
content/NKE-owned checks. Review then corrected wording inside the producer's
command comment. Exact command strings enter Build identity, so Run02 executed
the final command again through the existing native NKD runner. Its build and
both content probes passed separately; Run01 is not evidence for Run02's Build.

Final authored registry SHA-256:
`bfd87776dfd17c9158a80febf32705f5932f9aecee6fcd0f18068f1a59ac6713`.
Definition SHA-256:
`1b725124b74f4ed708cff9dca79df66b1a9cf2df28295d4ee5bf4111706f049b`.
Native/implementation-worktree read-back matched both files.

Native export:
`/mnt/lima-colima-nkd-demo/nkd-local/nke-recipes-profiles-20261008-01`.
Final attempt: `laptop-colima-profiles-arm64-20261008-02`.
Docker 29.5.2, native ARM64, dedicated Colima `nkd-demo`, eight CPUs and 24 GiB
configured RAM. Native storage was 81 GiB free before this small build; no fresh
OS bake ran and retained disk/source/artifact bytes were not edited.

| Identity | Value |
| --- | --- |
| NKE source | `8fb2d53368823bd386b4fbd55b4ab6d3c0931345` |
| NKE tree | `36569cf4ec0e2360ee023cd7e53da65b96ce770d` |
| Build | `sha256:48bf27682b10fa3f3fe65111cdebaa9ee823a206dbdd92b758a3ca44569c145a` |
| Plan (`planDigest` in the local record) | `sha256:e91c161752693393c13b7dabc4b34b7fbf1dc221c0e8c067c370d8c09f11abd8` |
| Observed image manifest | `sha256:e39ed458fea74cac2f5720c798d16c6d4dc863741ac1c5f93fa28bd004b6746f` |
| Image config | `sha256:6f7c5a51fb34abc62f0b38e731e994d6541489c5caa2871faa5ed86696895790` |

Run01 used Build `f68056d2cb438b98adc84693eb67806208bcbb59bacfaf02f145e6a374867cef`
and image manifest `b131765761da7f8fce04bde371f81ee903cc3399b15cee134883dad8ccaaadcc`.
The image subjects differ. This is not an unchanged-graph reproduction test;
neither build is qualified.

The existing catalog pin owner resolved/wrote the source commit/tree/epoch;
the lock was not hand-edited. The local Git override supplied pinned committed
objects from the NKE checkout. The existing launcher bootstrapped native NKD
and executed its normal fetch, source verification, BuildKit producer, terminal
observation, composition SBOM and OCI packaging owners.

```sh
CONTAINER_RUNTIME=docker NKE_LOCAL_CLONE=/Users/ikislenko/Workspace/github/nke \
  bash scripts/nke-full-arm64-local.sh laptop-colima-profiles-arm64-20261008-01 1 profiles
```

This launcher invocation ran inside Colima's native export for Run01. Run02
reused its measured `.nkd/laptop-bootstrap/laptop-colima-profiles-arm64-20261008-01/nkdctl`
and called `run local` with the final Definition/Environment, `--runner-source .`,
`--jobs 1 --runtime docker`, a fresh attempt, and the same Git transport override.
Final record:
`.nkd/runs/laptop-colima-profiles-arm64-20261008-02/lifecycle.json`,
`status: complete`, `lane: unqualified-local-execution`, 1/1, `failure: null`.

## Actual image and NKE-owned checks

Docker loaded the retained producer OCI tar without conversion. Docker 29's
load readback used the manifest identity, not its config digest. An initial
config-digest invocation for Run01 failed with “No such image”; the corrected
manifest invocation exited 0. The separate Run02 manifest invocation also passed.
No image byte changes resolved the invocation mistake.

The probe used `--rm --pull=never --network=none --read-only --cap-drop=ALL
--security-opt=no-new-privileges`. It read Alpine 3.24.2, the single ARM64/26.04
metadata entry and the exact local boot script, asserted two config profiles
and nine runbooks, refused the HTTP agent override and LUKS key, and found the
source TPM plugin configuration. The base is the existing maintained Alpine
digest, not the fixture's mutable 3.24.1 tag.

The same offline image streamed `/profiles` into a new private validation
directory. The accepted NKE source closure was copied, not modified. The
diagnostic `scripts/testdata/nke-local-profile/contract_test.go` was copied only
into that private source tree's `controller/internal/utils` package. Go 1.27.1
from the measured NKD bootstrap executed NKE's module with its existing pins;
it downloaded testify/YAML dependencies for this diagnostic, not another image
build. `GOENV=off`, `GOWORK=off`, `GOTOOLCHAIN=local` and a 180-second external
deadline were selected.

```text
=== RUN   TestNKDLocalProfile
=== RUN   TestNKDLocalProfile/Calico
=== RUN   TestNKDLocalProfile/OVN
--- PASS: TestNKDLocalProfile
PASS
ok github.com/dsx-ai-factory/nke/controller/internal/utils
```

NKE owns metadata parsing and the `quote`, `indent`, `trimPrefix` template
functions. Both rendered cloud-configs decoded as YAML and selected the stated
Kubernetes/CNI package commands. No NICoSim iPXE protocol, cloud-init execution,
registry reachability or kubeadm result is established by this test.

Probe containers exited/removed automatically. The accepted Run, loaded
diagnostic image, bootstrap and separate validation directory remain for
integration. No default Docker context, shared cluster, remote registry or
NKE source checkout was changed.

## Repository checks

`make check`, native CLI build, uncached `go test ./... -count=1`, focused
profiles compiler/preflight tests, formatting and `git diff --check` passed.
Lint reported zero issues and the Go vulnerability gate reported none.
Independent read-only review found no remaining actionable findings after the
agent/APT wording correction and exact-identity rerun. These checks do not expand
the runtime acceptance scope above.
