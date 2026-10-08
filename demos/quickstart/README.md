# Build your first artifact

This demo turns a tiny greeting script into a versioned executable through a real NVKE Recipes Run.
It takes one source, one catalog component, and one requested output. Docker performs the build;
NVKE Recipes pins the inputs, plans the work, checks the produced bytes, and records the result.

From the repository root, with Docker running and the Go version in [`.go-version`](../../.go-version)
on your `PATH`:

```sh
make build
bash demos/demo-local.sh --pause
```

Git and Python 3 are also required. Omit `--pause` to run straight through. The first invocation
may download the small digest-pinned Alpine image and Go dependencies. Rehearse once before presenting.
`NKDCTL_BIN` and `GO_BIN` can select already installed executables.

The script prints four short steps and keeps complete logs. Expect:

```text
1 / 4  Pin the source
2 / 4  Choose the output and see the plan
3 / 4  Run it
4 / 4  Check the result and use it
  Run: complete | output: hello | lane: unqualified-local-execution
  Artifact bytes match the recorded SBOM subject.
Hello, team! This is hello v1.0.0, built by NVKE Recipes.
```

## Read the inputs

| Input | What it says |
|---|---|
| [Catalog](catalog/registry.yaml) | Get `hello` from a pinned Git commit. Build it in pinned Alpine by substituting the tag into the script. |
| [Definition](definition.yaml) | Produce one executable output named `hello` from the `hello` component. |
| [Environment](environment.yaml) | Use Docker and a pinned utility container. |
| [Source](source/hello.sh) | Print a greeting with a version placeholder. |

The script creates a small local Git repository with two tags, then runs `catalog pin` to generate
`catalog/pins.lock.yaml`. Its `example.invalid` source URL is redirected to that local repository
for this process only. No GitHub account, registry credentials, or NKE checkout is needed.

The target is `linux/amd64`; Docker Desktop on Apple Silicon uses its Linux emulation support.
The resulting application is a portable shell script, so the demo also runs it directly on the host.

## See what changed

Run the second source version in another fresh workspace:

```sh
bash demos/demo-local.sh --version 1.1.0
```

The greeting changes to `Welcome`, and the version becomes `v1.1.0`. Compare the printed Build
digests: the changed source pin changes the build's identity. Each invocation retains its inputs
and results under a new `.nkd/demo-local.*` directory; the script prints the exact path.
Use `--out NEW_DIR` to choose a new directory yourself.

## Explore the result

Inside the printed workspace:

| File | What to inspect |
|---|---|
| `catalog/pins.lock.yaml` | The exact source commit, tree, tag, and timestamp. |
| `plan.json` | The compiled inputs, Build digest, operations, and artifact relationships. Load it into the [Plan explorer](../demo-team-explorer.html). |
| `plan.mermaid` | The exported graph for a Mermaid viewer. |
| `.nkd/runs/demo/lifecycle.json` | The complete Run, its requested output, and its observed artifact. |
| `.nkd/runs/demo/hello/out/hello/hello` | The executable produced by the container build. |
| `.nkd/runs/demo/hello/sbom/index.json` | The composition record bound to that executable's exact bytes. |
| `logs/run.log` | Full execution output, including each completed operation. |

This local Run is **unqualified local execution**. Its composition SBOM does not perform dependency
scanning. Nothing is signed or published. Continue with the [release walkthrough](../team-guide.md)
to inspect a hosted release and explain the separate signature and consumer-policy checks.
