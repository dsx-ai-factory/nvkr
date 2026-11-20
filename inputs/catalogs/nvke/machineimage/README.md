# NVKE MachineImage catalog candidate

This catalog-local Go program renders the maintained `machineimage.yaml.tmpl` and checks each
result against the MachineImage CRD from the catalog's pinned NVKE controller source commit.
This is an offline catalog experiment in a separate Go module, not an NVKR lifecycle capability or a registered
producer. No Definition or generated workflow invokes it. Production rendering,
signing, import and registration remain disabled/unimplemented.

Use Go 1.27.4 (the repository's `.go-version`) and Git 2.45 or newer with
`--no-lazy-fetch` support. The separate `go.mod` and
`go.sum` pin the schema validator and all transitive dependencies; the root module
does not import this renderer. Acquire the Go modules once before working offline.
For example, from this directory:

```sh
GOTOOLCHAIN=local go mod download
GOTOOLCHAIN=local GOPROXY=off go run . \
  --nvke-source /path/to/nvke-git-repository \
  --nvke-commit 2cfd00081b468d85a4894ec17e9deac13c9d8169 \
  --publication /path/to/primary-boot-description.json \
  --metadata /path/to/proposed-disk-metadata.json > candidate.yaml
```

The source can be a normal or bare Git repository containing the `nvke-controller`
commit in the adjacent `../pins.lock.yaml` (currently NVKE v0.23.0 at `2cfd0008`).
The program always reads that catalog pin; optional `--nvke-commit` must match it.
When invoking a compiled binary from another directory, set `--catalog-dir` to this
directory, which owns the template and adjacent catalog pin file.
It reads `management-engine/config/crd/bases/internal.nvke.nvidia.com_machineimages.yaml`
using `git --no-replace-objects --no-lazy-fetch cat-file blob '<pin>^{commit}:<path>'`.
Before reading source objects, it checks support for `--no-lazy-fetch`; older Git
versions fail closed instead of ignoring an unfamiliar environment variable. It
checks the unpeeled pin's object type and rejects annotated tag objects. It never
uses a dirty working-tree copy, fetches a revision or vendors the CRD. Every child
Git process also sets `GIT_NO_LAZY_FETCH=1`, so missing objects in partial clones
cannot trigger retrieval from a promisor remote. A missing commit or CRD fails closed.

Repeat `--publication` for each destination. Each input is the JSON description from
the generic `PreparedBootAssets.Description` or `SelectedBootReceipt.Description`
API: `selection.disks`, `selection.imagers`, `destination.name`, and `assets` containing
`binding` plus `url`. Named raw bindings supply platform, member/profile, SHA-256 and
size. One selected imager must match each disk architecture, and all selected assets
must be present exactly once. These descriptions cannot restore receipts and the
program does not verify signatures, Release membership, object-store bytes or URL
availability. No CLI export/verification adapter is introduced here.

Supply a JSON array of proposed metadata keyed by exact disk output and platform:

```json
[{"output":"node-image","platform":"linux/amd64","family":"ubuntu","version":"24.04","variant":"","buildDate":"2026-11-19"}]
```

All six fields are required; an empty variant omits that field in the CR. Every selected
disk needs one entry, with no duplicate or unused entries. Metadata is descriptive:
binding it to authenticated producer evidence remains future integration work.

The candidate emits one CR per disk/platform/destination in sorted order, after the
whole set passes validation. Names fingerprint exact pre-name YAML bytes; they are
neither NVKR Build Digests nor Release identities. Names differ from the previous
Python renderer because Go serializes YAML differently; nothing consumes these candidate names.
Destination/output and imager raw digest annotations distinguish pairs without embedding final aggregate/inventory
identities. Inputs and errors release no partial manifest set. The template retains
literal `{{ .DiskSelectionStrategy }}` and `${cloudinit-url}` for downstream use;
URLs have a restricted literal HTTPS grammar to prevent script/template injection.

Validation uses the served `v1` CRD's OpenAPI schema through `k8s.io/kube-openapi`,
including required fields, types, length bounds, patterns and date formats. Supported
schema features are explicitly admitted; unknown features and references fail closed.
A guarded table maps exact CEL rule text and schema location to Go predicates for
the iPXE header, nonempty command and balanced template delimiters. Any unknown CEL
rule anywhere in the CRD fails closed. The known `self == oldSelf` transition rule
does not apply when creating a candidate. This does not run a CEL interpreter or
Kubernetes defaulting, pruning, admission or update-immutability checks. It
does not establish an admitted imager argument/profile contract, firmware trust,
checksum-before-write behavior or successful boot. Those require the EFI producer
and NVKE integration work plus amd64/arm64 external evidence.

Run the focused contracts against synthetic schemas, or opt into that same pinned
source without a vendored CRD:

```sh
GOTOOLCHAIN=local go test ./... -count=1
NVKE_SOURCE=/path/to/nvke-git-repository GOTOOLCHAIN=local go test ./... -count=1
```

`make test-ci-tools`, included in `make check` and the merge gate, runs the synthetic
contracts in this nested module. Root `go test ./...` does not traverse nested modules.
This directory is not referenced by a producer, so changing it does not change existing Plan or Build Digests.

Producer/toolchain admission remains separate from this experiment's hash-locked dependencies.
