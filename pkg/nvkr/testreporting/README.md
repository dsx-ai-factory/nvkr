# Verified observation reporting

This package ingests every input through stock `testobservation.VerifyOCI` before source-specific deduplication. Generated JSON, Markdown and TestGrid artifacts describe authenticated historical observations; they establish no current health, qualification, receipt restoration or service publication.

The [operator guide](../../../docs/portable-test-evidence-execution.md) owns invocation examples, exact external TestGrid source pins, backend registration, destination/retention selection and external acceptance gaps. Tests in this package use private injected receipt fixtures; bundle tamper authentication is tested by the stock owner.

Community paths use a domain-separated `nvkr.community-signer-label` canonical grouping digest of the authenticated signer identity and issuer plus the exact Definition. This deterministic, unsalted label is enumerable and supplies no confidentiality or authorization. Raw signer identity and issuer remain disclosed in JSON, Markdown and evidence sidecars. The collision guard is defence in depth.

At most 128 inputs, 256 MiB of covered bytes and 512 covered files (including observation control files) are retained. One bounded input inventory can be temporarily held before an aggregate gate rejects and closes it.

Exports use private directories (0700) and files (0600). They never upload; `Published` stays false. `reportCommitted` and `testGridCommitted` distinguish actual no-replace commits from requested paths. A late failure reports committed state explicitly and performs no rollback. Known admission failures occur before any output commit.

Multiple original JUnit files use full filename-digest transport labels. TestGrid may display those opaque hashes as filename-derived contexts; `artifacts/evidence.json` maps each back to its original covered filename. Backfill visibility depends on the selected TestGroup's retention and build ordering. Destination and retention choices belong to a future explicitly configured uploader; neither is inferred by this export-only API.
