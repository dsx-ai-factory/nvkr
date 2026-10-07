# Recorded observations

These records preserve external service results, expensive image builds and boot checks, and useful
interoperability investigations. Each applies only to its stated source, subjects, date and evidence lane.
Current support belongs in [reconciliation](../adr/reconciliation.md); ordinary contract behavior belongs
in code/tests and current CI, rather than accumulated gate transcripts.

| Record | Source and observed result | Scope and limits |
|---|---|---|
| [Signed CLI, September 10](2026-09-10-github-signed-run.md) | `6954dfc`; keyless hosted signing and independent Linux consumption | `pipeline-attested/artifact-publication`; earlier valid signatures do not turn a failed Run into success |
| [Combined NKE release, September 10](2026-09-10-combined-run.md) | `4557f64`; CLI/image/chart publication, verification and independent payload checks | Same consumer scope; recorded build-type claim was too strong; no qualified reproduction or deployment |
| [Local ARM64 image, September 23](2026-09-23-nke-arm64-local-os-build.md) | `05c2037` plus identified draft; actual TCG image build and boot smoke | `unqualified-local-execution`; does not accept later source revisions |
| [Native ARC images, October 1–2](2026-10-01-nke-arc-os-builds.md) | Exact listed candidates/runs; both architectures baked and booted | Diagnostic local lifecycle on ARC; not generated signing/publication or qualified acceptance |
| [M2 candidate release, October 6](2026-10-06-team-demo-pipeline.md) | `625980e`, Run 37533806963; 34-output finalization and signing, separately scoped metadata browsing | `pipeline-attested`, disks use build provenance; no independent full-payload disk verification |
| [Early hosted failures](hosted-release-history.md) | September 9–11 exact commits; missing credentials, dry-run publication, mixed-attestation failure | Keeps failures distinct from signed success and local-key interoperability |
| [Producer/acquisition observations](producer-acquisition-observations.md) | September 10–October 6 source-specific rendering, chart preservation and upstream metadata reads | Local/unqualified or read-only; capture correspondence is not original live-disk evidence |
| [Disk inspection/codec observations](disk-inspection-observations.md) | October 1–5 exact disk/tool/host-source subjects; native ARM64 scans, cancellation and QEMU interoperability | Installed metadata and retained-disk diagnostics; unchanged helper and missing original live-Run joins |
| [Hosted timing comparison](2026-09-10-github-run-performance.md) | `c15e534` versus `6954dfc`; measured artifact-workflow timing | Individual September runs, including dry-run versus real signing; not current M2 or Merge Gate performance |
| [Presentation validation](2026-10-07-team-presentation-validation.md) | PR #108 CLI and separately scoped October 7 presentation edits | Browser/lab validation, not new production execution or consumer acceptance |

Keep meaningful raw Run, verification and payload-check records with their narrative. The M2
[capture manifest](2026-10-06-team-demo-pipeline/sources.json) binds the original 22 retained files;
its adjacent presentation exports remain because the original demo uses that snapshot. Hashes identify
captured bytes; they do not independently authenticate a signing or policy decision. Local artifacts and
expiring Actions links are explicitly distinguished from retained captures. Superseded routine review,
fixture and gate progress is available in Git history.
