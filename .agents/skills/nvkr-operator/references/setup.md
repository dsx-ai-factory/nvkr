# Acquire the selected CLI

Preserve the repository URL and revision the customer requested. Read its README and operator skill before
following setup instructions. For the maintained project, the entry is [NVKR on
GitHub](https://github.com/dsx-ai-factory/nvkr); a fork is not interchangeable. If the customer supplied a
branch, tag, or commit, use that exact selection. Otherwise fetch the current default branch and record its
full resolved commit before building; call it current source, not an approved release. Do not switch an
explicitly requested ref to main.

Inspect an existing checkout's remote, branch, HEAD, and `git status --short` before reuse. Preserve customer
edits and concurrent work. Use a separate checkout/worktree for a different ref or when edits would overlap;
never reset, clean, discard stashes, or overwrite an existing directory. For a fresh checkout, use `git clone
<requested-url> <new-directory>`. The repository may require authenticated GitHub access; report access
failures as setup blockers, without substituting a mirror or fork or asking for secrets in chat. Resolve and
inspect the chosen revision there; use the same revision's docs throughout.

Choose the setup route from the requested task guide first. The
[quickstart](../../../../docs/quickstart.md) requires Git, Bash, Go and Docker; Make is not required.
Its launcher checks prerequisites, builds NVKR from source with the exact compiler
selected automatically, then runs the three-output NVKE build. It runs in a child
shell and does not change the caller's shell settings. If Go is missing it offers a checksum-verified
checkout-local SDK installation; `--install-go` explicitly authorizes that local installation without
an interactive prompt. Do not approve system package installs without customer authorization.
Do not paste `set -e` into
an interactive SSH shell.
For CLI setup without executing producers, use the host build route below; it requires an existing Go installation.
If another selected guide supplies a pinned compiler bootstrap, inspect and follow
its exact source and tool identity rather than substituting an arbitrary image.

For the host build route, read `.go-version`, `Makefile`, and [DEVELOPMENT.md](../../../../DEVELOPMENT.md)
from the repository root. Check the host platform, Git and Go. In the setup shell, select the
checkout's exact compiler automatically and keep that selection for later build-helper compilation:

```sh
export GOTOOLCHAIN="go$(cat .go-version)"
NVKR_VERSION="$(git describe --tags --always --dirty)"
NVKR_COMMIT="$(git rev-parse --short HEAD)"
go build -ldflags "-X main.version=$NVKR_VERSION -X main.commit=$NVKR_COMMIT" -o bin/nvkr ./cmd/nvkr
bin/nvkr --version
bin/nvkr --help
```

Go downloads that exact compiler when necessary. No manual patch-version installation or global
Go/PATH configuration change is needed. Use `bin/nvkr` directly from the repository root.

Record the full `git rev-parse HEAD` together with the CLI stamp. A dirty tree is not equivalent to that
committed revision; disclose local changes. If an existing binary's provenance cannot be matched to the
requested source, rebuild instead of relying on its filename or abbreviated version alone. Do not replace the
supported Makefile build with an unstamped convenience build. A maintained bundle can deliberately require
`dev`/`unknown` generator stamps: follow [catalog maintenance](../../../../docs/catalog-maintenance.md) or
`make workflows-check` for that case, recording both the inspected full source commit and required generator
identity.

A missing prerequisite is a concrete setup blocker, not permission to alter the customer's system. Reuse a
matching installed toolchain where available; the source-build route explicitly selects the compiler
pinned by the checkout and may download it. Install other missing tools only within the customer's
authorized setup scope, without modifying global configuration implicitly. Do not weaken the version
gate or substitute an unpinned compiler.

Only check heavier prerequisites for the selected task. Read the quickstart and selected
Definition/Environment guide for Docker or podman, native VM/KVM tools, source credentials, and hosted signing
or verification requirements. Building the CLI may download dependencies; it does not build NVKE artifacts,
launch a demo, or publish anything. macOS development/planning support does not admit Linux authority
operations.

For a setup-only request, stop after version/help verification and summarize what is ready. For a concrete
task, continue into the skill's authoring or operation path.

## Client discovery

[Codex documentation](https://learn.chatgpt.com/docs/build-skills) specifies `.agents/skills/` discovery from
the working directory toward the repository root. [Claude Code
documentation](https://code.claude.com/docs/en/skills) specifies `.claude/skills/`; this repository does not
install that client location. Both can read the README's explicit operator-skill link when instructed to use
the repository. No cross-client automatic selection or complete URL-only agent execution has been demonstrated
by the local CLI exercises.
