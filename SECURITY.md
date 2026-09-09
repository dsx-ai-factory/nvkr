## Security

NVIDIA is dedicated to the security and trust of our software products and services, including all source code repositories managed through our organization.

If you need to report a security issue, use the contact points below. **Do not report security vulnerabilities through public GitHub issues, discussions, or pull requests.** If a potential security issue is disclosed publicly, NVIDIA maintainers may limit public discussion and redirect the reporter to the private reporting channels.

## Reporting a potential security vulnerability in an NVIDIA product

To report a potential security vulnerability in an NVIDIA product:

- Web: [Report a Security Vulnerability or NVIDIA AI Concern](https://www.nvidia.com/en-us/security/report-vulnerability/)
- Email: [psirt@nvidia.com](mailto:psirt@nvidia.com)
  - NVIDIA encourages encryption with its [public PGP key](https://www.nvidia.com/en-us/security/pgp-key/).
  - Include the affected product or driver and its version or branch, the type of vulnerability, reproduction instructions, proof-of-concept or exploit code when available, and the potential impact.

NVIDIA follows a coordinated vulnerability disclosure process. See the [NVIDIA PSIRT policies](https://www.nvidia.com/en-us/security/psirt-policies/) for details.

## NVIDIA Product Security

For security bulletins, policies, acknowledgements, and reporting resources, visit the [NVIDIA Product Security portal](https://www.nvidia.com/en-us/product-security/).

## Scope

nkdctl's target trust model is **reproducibility as provenance**. The currently implemented authority-bearing
surface is narrower: contract-tested Linux local qualification and exact-byte verification require authenticated
profiles, descriptor-held pinned tools, terminal artifact receipts, and signed typed evidence; unavailable
adapters fail closed. This is not a claim that a production deployment or its external services have been
qualified. Reports of particular interest:

- a way to make `verify` **pass** on artifacts that do **not** match the signed manifest (a fail-**open** hole),
- a path that lets an unpinned source ref, a tag-pinned toolchain, or a reserved axis **build instead of
  failing closed**,
- non-determinism that lets host/clock/random state leak into the IR, a digest, an SBOM, or a signature,
- signature/offline-verify bypasses involving authenticated documents, pinned roots, bundles, issuer, or
  identity policy.

Out of scope: vulnerabilities in the **upstream components** nkdctl builds (report those upstream) and in
an **operator-authored catalog** (`--data`) that ships outside this repository.

## Trust boundaries and deployment assumptions

Qualified paths treat catalogs, signed documents, detached signature material, producer results, fetched
content, artifact paths, mirror state, and publication inputs as untrusted until their owning module has issued
an opaque receipt over the exact bytes and policy. Mutable filesystem names are not authority: standalone
authority subprocesses whose protocols accept descriptor paths use held or anonymous file descriptors for the
executable and security-sensitive inputs. A platform that cannot execute the held capability must fail that
qualified operation rather than fall back to an ambient or private pathname. Container execution is a distinct
engine boundary: the configured daemon and its mount implementation are trusted to consume the materialized,
receipt-bound source tree presented by the runner; NKD does not claim a pathname bind mount is an FD transfer.

That hardening is not a sandbox for the account running nkdctl. The trusted computing base still includes the
host kernel, the integrity of the running nkdctl process, root-owned loader and library state, the configured
container-daemon endpoint, trusted time and trust-store configuration, and every explicitly injected in-process
adapter. Arbitrary code that can ptrace the process, replace its memory, control its container daemon, or obtain
its inherited descriptors can act with the same authority. Run qualified producer and verifier roles under
dedicated service identities or isolated machines, protect the runtime socket, prohibit untrusted same-account
processes, and prefer static externally pinned tools. Cross-machine producer, scanner, signer, time, and registry
claims require authenticated protocols; a Go interface alone cannot prove that its implementation performed the
claimed I/O.

Descriptor-held executable invocation is currently an authority capability on Linux. macOS has no equivalent
`fexecve`/`execveat` primitive and its descriptor filesystem is not executable, so qualified subprocess paths
that require exact executable-byte authority fail closed there. Read-only diagnostic operations may have a
broader platform surface, but their outputs cannot authorize attestation, release, or publication.

## Supported versions

nkdctl is pre-1.0 (POC); security fixes target `main`. Pin by commit and rebuild from source to verify.
