# Acquire the selected CLI

Preserve the repository URL and revision the customer requested. Read its README and operator skill before following setup instructions. For the maintained project, the entry is [NVKR on GitHub](https://github.com/dsx-ai-factory/nvkr); a fork is not interchangeable. If the customer supplied a branch, tag, or commit, use that exact selection. Otherwise fetch the current default branch and record its full resolved commit before building; call it current source, not an approved release. Do not switch an explicitly requested ref to main.

Inspect an existing checkout's remote, branch, HEAD, and `git status --short` before reuse. Preserve customer edits and concurrent work. Use a separate checkout/worktree for a different ref or when edits would overlap; never reset, clean, discard stashes, or overwrite an existing directory. For a fresh checkout, `git clone <requested-url> <new-directory>` is sufficient. Resolve and inspect the chosen revision there; use the same revision's docs throughout.

From the repository root read `.go-version`, `Makefile`, and [DEVELOPMENT.md](../../../../DEVELOPMENT.md). Check the host platform, Git, Make, and `go version`. The build requires the exact Go patch from `.go-version` and access to its pinned Go dependencies. Do not hardcode a historical Go version. Build using:

```sh
GOTOOLCHAIN=local make build
bin/nvkr --version
bin/nvkr --help
```

Record the full `git rev-parse HEAD` together with the CLI stamp. A dirty tree is not equivalent to that committed revision; disclose local changes. If an existing binary's provenance cannot be matched to the requested source, rebuild instead of relying on its filename or abbreviated version alone. Do not replace the supported Makefile build with an unstamped convenience build.

A missing prerequisite is a concrete setup blocker, not permission to alter the customer's system. Reuse a matching installed toolchain where available; report the required version and installation options when a new tool is needed. Install only within the customer's authorized setup scope, without modifying global configuration implicitly. Do not weaken the version gate or let Go silently select another toolchain.

Only check heavier prerequisites for the selected task. Read the quickstart and selected Definition/Environment guide for Docker or podman, native VM/KVM tools, source credentials, and hosted signing or verification requirements. Building the CLI may download dependencies; it does not build NVKE artifacts, launch a demo, or publish anything. macOS development/planning support does not admit Linux authority operations.

For a setup-only request, stop after version/help verification and summarize what is ready. For a concrete task, continue into the skill's authoring or operation path.
