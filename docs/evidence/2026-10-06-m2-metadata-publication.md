# M2 metadata publication contracts

This records local contract validation for the uncommitted `feat/m2-native-arc` changes
based on `73395149a8fd3435c7cda7ba466a566bffc79b7d`. It is not a hosted release or
live signing acceptance record.

The M2 Environment selects `publication.mode: per-output-v1`. Its generated Plan has
34 named outputs and 35 publication routes: one per output and one aggregate release.
Each output retains payload verification. Aggregate assembly and finalization consume
authenticated metadata and evidence; the finalizer restores six carriers. The
main-only dispatch/signing policy remains in force.

## Generated workflow

- Plan digest: `sha256:aa0d12191f31fb5daf4ac42041fea8b9abd7d63362475cf2ca567c1b7c772c77`.
- Workflow size: 465,535 UTF-8 bytes, below the generator's 500,000-byte limit.
- `run generate --check` with the M2 Definition and GitHub Environment passed.
- `actionlint` passed. `yamllint` passed with generated-format allowances for line
  length, the `on` key and the optional document-start marker.
- Size tests cover one byte below, exactly at and one byte above the limit, multibyte
  characters, and rejection before any generated output is written. Compaction checks
  deterministic output and exact equivalence of the expanded YAML documents.

## Publication contracts

Loopback-registry tests cover AMD64 and ARM64 disk outputs: metadata pull and push
perform zero payload GETs, while the existing full consumer pull retrieves and checks
the payload. Metadata inspection cannot issue the full-payload validation result.

Factory tests cover exact output selection, shared evidence, six-carrier finalization,
complete provider-job outcomes, wrong-run rejection, signature and SBOM rejection,
and dry-run rejection. Cryptographic adapters in these factory tests are explicit test
doubles; they do not demonstrate Fulcio, Rekor or GHCR operation.

## Check status

Focused contracts, formatting, diff checks, Go vet, Linux-targeted lint, shellcheck and
the vulnerability scan passed with Go 1.27.1. The full non-race run found one
formatting-dependent credential-test mutation, corrected to mutate parsed YAML and
then recompact; the corrected test passed. Linux race tests passed for `sign`,
`delivery`, `factory` and `lifecycle` with bounded package parallelism. The full
macOS race gate (`go test -race -p 2 ./... -count=1`) passed.

macOS lint reports five existing staticcheck SA4023 findings in platform-dependent
pipeline stubs. Linux-targeted lint reports zero findings. Initial overloaded test
runs timed out; an initial Linux run also used a read-only source mount incompatible
with a lifecycle test. The replacement Linux run uses a writable isolated source copy.

Hosted storage peaks, execution time, internal-registry permissions and live signing
for this publication mode remain unmeasured. Per-output jobs still need sufficient
space for their individual payloads.
