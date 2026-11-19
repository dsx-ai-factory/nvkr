# NVKE MachineImage catalog candidate

`render_candidate.py` renders the maintained `machineimage.yaml.tmpl` and checks each
result against the MachineImage CRD from the catalog's pinned NVKE controller source commit.
This is an offline catalog experiment, not an NVKR Go capability or a registered
producer. No Definition or generated workflow invokes it. Production rendering,
signing, import and registration remain disabled/unimplemented.

Use Python 3.10 or newer, Git, and the dependencies in `requirements.txt` in an
isolated environment. For example, from this directory:

```sh
candidate_venv="$(mktemp -d)/venv"
python3 -m venv "$candidate_venv"
"$candidate_venv/bin/python" -m pip install -r requirements.txt
"$candidate_venv/bin/python" render_candidate.py \
  --nvke-source /path/to/nvke-git-repository \
  --nvke-commit 2cfd00081b468d85a4894ec17e9deac13c9d8169 \
  --publication /path/to/primary-boot-description.json \
  --metadata /path/to/proposed-disk-metadata.json > candidate.yaml
```

The source can be a normal or bare Git repository containing the `nvke-controller`
commit in the adjacent `../pins.lock.yaml` (currently NVKE v0.23.0 at `2cfd0008`).
The script always reads that catalog pin; optional `--nvke-commit` must match it.
It reads `management-engine/config/crd/bases/internal.nvke.nvidia.com_machineimages.yaml`
using `git --no-replace-objects cat-file blob '<pin>^{commit}:<path>'`, which requires
an actual commit and ignores replacement objects. It never uses a dirty working-tree
copy, fetches a revision or vendors the CRD. The child Git process sets
`GIT_NO_LAZY_FETCH=1`, so missing objects in partial clones cannot trigger retrieval
from a promisor remote. A missing commit or CRD fails closed.

Repeat `--publication` for each destination. Each input is the JSON description from
the generic `PreparedBootAssets.Description` or `SelectedBootReceipt.Description`
API: `selection.disks`, `selection.imagers`, `destination.name`, and `assets` containing
`binding` plus `url`. Named raw bindings supply platform, member/profile, SHA-256 and
size. One selected imager must match each disk architecture, and all selected assets
must be present exactly once. These descriptions cannot restore receipts and the
script does not verify signatures, Release membership, object-store bytes or URL
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
neither NVKR Build Digests nor Release identities. Destination/output and imager raw
digest annotations distinguish pairs without embedding final aggregate/inventory
identities. Inputs and errors release no partial manifest set. The template retains
literal `{{ .DiskSelectionStrategy }}` and `${cloudinit-url}` for downstream use;
URLs have a restricted literal HTTPS grammar to prevent script/template injection.

Validation uses the served `v1` CRD's OpenAPI schema through JSON Schema Draft 4,
including required fields, types, bounds, patterns and date formats. It does not run
Kubernetes CEL, defaulting, pruning, admission or update-immutability checks. The
candidate separately checks the iPXE header and balanced template delimiters. It
does not establish an admitted imager argument/profile contract, firmware trust,
checksum-before-write behavior or successful boot. Those require the EFI producer
and NVKE integration work plus amd64/arm64 external evidence.

Run the focused contracts against that same source without a vendored schema:

```sh
PYTHONDONTWRITEBYTECODE=1 "$candidate_venv/bin/python" test_render_candidate.py \
  --nvke-source /path/to/nvke-git-repository \
  --nvke-commit 2cfd00081b468d85a4894ec17e9deac13c9d8169
```

These standalone tests require the supplied source and Python dependencies; the
repository's default Go suite does not run them. This directory is not referenced
by a producer, so changing it does not change existing Plan or Build Digests.

Dependency follow-up: lock transitive Python dependencies and distribution hashes before
producer/toolchain admission. `requirements.txt` currently pins only the direct packages.
