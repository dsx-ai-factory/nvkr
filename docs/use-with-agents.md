# Use NVKR with Your AI Agent

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

Supply the output you want to add and the paths to your Definition and Environment.
The [catalog guide](catalogs-and-components.md) explains components, default and per-output catalog
stacks, overlays and supported production mechanisms.

**Update a pin:**

```text
Use https://github.com/dsx-ai-factory/nvkr to preview an update of the source I name
in my catalog to the exact release I provide. Show the proposed pin changes and
explain their expected Plan impact without applying them or publishing anything.
```

Supply the catalog location, source and release, plus the Definition and Environment whose Plan impact
you want explained. The skill directs the agent to use the CLI's pin commands rather than editing locks
by hand. [Catalog refreshes](catalog-maintenance.md) covers coordinated updates and regeneration.

**Build the NVKE tutorial artifacts locally:**

```text
Use https://github.com/dsx-ai-factory/nvkr to follow the quickstart in a fresh
workspace. Check prerequisites, build NVKR from source, then build all three
selected NVKE artifacts locally. Report the output locations, Run result and
evidence limits. Do not deploy the NVKE demo or publish anything.
```

The [quickstart](quickstart.md#before-you-start) owns the prerequisites, platform limits and commands.
This example needs Git, Bash and Go, plus Docker Desktop on macOS or Docker on Linux.
If the pinned Go compiler is missing, the launcher offers a verified installation inside the checkout; Make is not required.
The launcher checks prerequisites before starting.
It downloads pinned inputs and retains
all three selected outputs locally. The build is unsigned and unqualified.

## What the agent does

A concrete task includes preparing the selected CLI through a local source build or the task guide's
documented bootstrap. This setup may clone the selected revision and download dependencies, even for
an input-edit task. Say so in your prompt if you want to prevent that setup or reuse an existing CLI
build whose source revision can be verified.

An input-edit request authorizes those input changes and validation, not an artifact build, publication
or deployment. Include execution or publication explicitly when wanted, with its selected inputs and
destination. The skill directs the agent to preserve your edits, ask only for a missing consequential
choice or authorization, and not treat missing tools as permission for global installation or configuration
changes. Keep your agent client's own permission controls enabled.

[Codex](https://learn.chatgpt.com/docs/build-skills) supports repository skill discovery from
`.agents/skills/` after checkout. [Claude Code](https://code.claude.com/docs/en/skills) uses a different
project-skill location; it and other agents can read the
[operator skill](https://github.com/dsx-ai-factory/nvkr/blob/main/.agents/skills/nvkr-operator/SKILL.md)
linked above directly. Automatic skill selection is not guaranteed, and a complete URL-only agent run
has not been verified. Point your agent at the linked skill if it does not discover it.

Ask for the exact source revision, CLI version, input diff, checks, output identities and remaining
blockers in the final report. A Plan or dry-run is `static-unqualified` (see [Concepts](user-concepts.md));
release browsing is metadata inspection. Built bytes, signatures, supported `pipeline-attested`
verification and qualification checks establish different facts. The [lifecycle guide](lifecycle.md)
owns current checks, policies and supported verification scopes, including the limits of qualification
and full-release acceptance.
