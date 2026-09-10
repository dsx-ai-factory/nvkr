# Reading the generated workflow

GitHub job names describe the operation; step names identify the selected component or output. Stable internal
node IDs remain in execution details and logs so an error can be joined to the generated plan.

For the maintained NKE Definition:

- **Fetch pinned source for cwe** and **Build cwe** identify the source and image build jobs.
- **Verify and package outputs with Helm image binding** groups offline artifact checks, SBOM generation and
  packaging on one runner. Every operation has its own named step and isolated execution command.
- **Bind image digest and package Helm: nke-helm** embeds the verified CWE manifest digest in the controller
  chart, refreshes checksums and packages the chart snapshot. It runs after image packaging and before publication.
- **Publish**, **Sign and attest**, and **Verify published signatures** run as separate jobs for each output.
- **Finalize Run and check completeness** recovers artifacts and evidence, reports each recovery outcome, and
  checks the complete Run even when a producer failed.

Each execution step prints its operation, node ID, successful output IDs and elapsed seconds. A failed command
prints its exit code and duration without claiming completion or swallowing the error. Packaging prints the
actual image digest it bound; publication prints the immutable reference only after persisting it. These logs
are diagnostics. Authentication still uses the existing exact-byte records and signature verification.

The factory contract suite exercises the emitted Bash with successful and failing executable fixtures and
checks the maintained workflow's labels, credential scope, execution order and transport. Helm tests compare
the logged binding to the inspected image digest. A local registry test checks the published reference log.
These checks do not establish a hosted run of this workflow revision.
