# Use NVKR with your AI agent

Give your agent the NVKR repository URL and a concrete task. It can follow the repository's
[operator skill](https://github.com/dsx-ai-factory/nvkr/blob/main/.agents/skills/nvkr-operator/SKILL.md)
to acquire the selected source revision, prepare the CLI, author inputs and operate supported workflows.
You do not need to assemble a command list or install a global skill first.

## Start with a task

Copy this prompt into a terminal-capable agent:

```text
Use https://github.com/dsx-ai-factory/nvkr to create a Definition and Environment
for a Linux ARM64 NVKE CLI executable in my project. Preserve existing edits,
validate the inputs, and show the Plan and diff. Do not run the artifact build yet.
```

Adapt the requested outputs, platform and project location to your task. If you need a particular
NVKR branch, tag or commit, include it; otherwise the skill records the resolved current source revision.
A repository URL by itself leaves the task unspecified.

The agent needs repository access, file access and a terminal to perform this workflow. If GitHub access
is denied, it should report that setup blocker without substituting a fork or asking you to paste a token
into chat. Authenticate through your approved local tools or credential references. The CLI's build
and runtime prerequisites depend on the chosen task; the skill follows its maintained setup guide.

## Example requests

**Change build inputs:**

```text
Use https://github.com/dsx-ai-factory/nvkr to add an output to my Definition and
select the Environment for local planning. Show affected outputs and Build Digests.
```

The [catalog guide](catalogs-and-components.md) explains components, default and per-output catalog
stacks, overlays and supported production mechanisms.

**Update a pin:**

```text
Use https://github.com/dsx-ai-factory/nvkr to preview an update of the source I name
in my catalog to the exact release I provide. Show the proposed pin changes and
explain their expected Plan impact without applying them or publishing anything.
```

Supply the catalog location, source and release. The agent uses the CLI's pin owner rather than editing
locks by hand. [Catalog refreshes](catalog-maintenance.md) covers coordinated updates and regeneration.

**Try one small local build:**

```text
Use https://github.com/dsx-ai-factory/nvkr to follow only the quickstart's first
hello-artifact build in a fresh workspace. Check prerequisites first, build and run
that example, and report its local artifact and evidence limits. Do not continue
into the NVKE tenant demo or publish anything.
```

The [quickstart](quickstart.md#build-and-use-your-first-artifact) owns the prerequisites and commands.
This example needs Docker and may download pinned inputs; it is a local build, not a signed release.

## What the agent does

Routine acquisition, prerequisite checks and the supported CLI build can proceed within your requested
setup scope. An input-edit request authorizes those input changes and validation, not an artifact build,
publication or deployment. Include execution or publication explicitly when wanted, with its selected
inputs and destination. The agent preserves your edits and asks only for a missing consequential choice
or authorization. Missing tools do not authorize global installation or configuration changes.

[Codex](https://learn.chatgpt.com/docs/build-skills) supports repository skill discovery from
`.agents/skills/` after checkout. [Claude Code](https://code.claude.com/docs/en/skills) uses a different
project-skill location; it and other agents can read the README's explicit operator-skill link directly.
Automatic selection across clients and a complete URL-only agent run have not been demonstrated by the
local CLI exercises. Use the linked skill directly if your agent does not discover it.

Ask for the exact source revision, CLI version, input diff, checks, output identities and remaining
blockers in the final report. A Plan or dry-run is `static-unqualified`; release browsing is metadata
inspection. Built bytes, signatures, supported `pipeline-attested` verification and qualification checks
establish different facts. The [lifecycle guide](lifecycle.md) owns current checks, policies and supported
verification scopes, including the limits of M3 qualification and full-release acceptance.
