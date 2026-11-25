# Validation planners

These authored inputs tell `nvkr validate --plan` how to turn one authenticated release into validation targets.
A plan is descriptive and carries no qualification authority. See the
[operator reference](../../../docs/external-test-output-contract.md#plan-what-to-validate-for-a-release).

| File | Purpose |
|---|---|
| `config.yaml` | The planners to run. Each entry pairs a built-in planner with the executor that runs its targets and the planner's own configuration |
| `nvke-qualify/planner.json` | The subject, scope and validator the `nvke-qualify` planner plans for |
| `nvke-qualify/subject.yaml` | NVKE-schema document: which components and dependencies the end-to-end suite is given |
| `nvke-qualify/policy.yaml` | NVKE-schema document: the streams and the scopes over them |
| `nvke-qualify/registry.yaml` | NVKE-schema document: how each component resolves to a version and a location |

The three `nvke-qualify` documents use NVKE's schema (`apiVersion: nvke.nvidia.com/v1alpha1`, with the kinds
`Qualification`, `QualificationPolicy` and `QualificationRegistry`), because NVKE's planner reads them. Their
kind names are NVKE's. They grant no qualification authority here, and the NVKR qualification policy that
authenticates a release is a separate document.

These files decide which artifacts are used and which configurations are covered, so they are authored and
reviewed like any other input and are never generated at run time. Only run-scoped values arrive with
`--planner-var planner.key=value`.

## The `nvke-qualify` planner

The release supplies the controller, the cluster API provider, the command-line binary for each architecture
and the node image for each architecture. A component's version is the digest of its release output and its
location is that output's reference. A node image has a location only, so a run can name the https URL a copy
is served at with `--planner-var nvke-qualify.overrides=os-image-<arch>=<url>`. The `overrides` variable takes
the form of NVKE's `qualify plan --overrides`, comma-separated `name=value` pairs, so a value cannot contain a
comma. NVKE's tree supplies `kind`, `clusterctl` and `nerdctl` through `nvke-qualify.nvke-root`. The planner
takes those two variables and rejects any other.

`kube` and `cni-ovn` are declared and not resolved. The release carries neither as an output, and NVKE's own
resolution of them reads lock files at an image tag, which an NVKR release does not provide. The plan records
exact versions that a reviewer wrote down, through the `nvkrDeclared` strategy: `cni-ovn` is the value in
`subject.yaml`, and `kube` is looked up in `registry.yaml` from the stream's Kubernetes minor, so a stream for a
minor with no entry fails the plan. The values are the ones NVKE locks at the tag the catalog pins (`v0.23.0`),
without a leading `v`. Nothing checks them against the node image, so a change to the image's Kubernetes or OVN
version, or to that pin, needs the same change in these files. NVKE's suite also takes versions as tags today,
so a plan's digest-valued versions describe a release and are not yet usable by that suite.

```sh
nvkr validate --plan \
  --reference <repository>@sha256:<digest> --policy inputs/policies/nvke-composite-qualification.yaml \
  --planners inputs/testing/planners --planner-var nvke-qualify.nvke-root=<path to an NVKE tree>
```
