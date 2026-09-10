# Native Node.js 24 GitHub actions

The successful signed Run `34422660746` still emitted Node.js 20 deprecation annotations. GitHub was already
forcing those older actions onto Node.js 24; the warning described their declared runtime. The
[GitHub migration notice](https://github.blog/changelog/2025-09-19-deprecation-of-node-20-on-github-actions-runners/)
requires upgrading to action versions that declare Node.js 24.

## Exact action pins

The official release tags resolved to these immutable commits. Each commit's `action.yml` declares
`runs.using: node24`:

| Action | Release | Commit |
|---|---|---|
| [checkout](https://github.com/actions/checkout/blob/3d3c42e5aac5ba805825da76410c181273ba90b1/action.yml) | v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| [setup-go](https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/action.yml) | v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` |
| [upload-artifact](https://github.com/actions/upload-artifact/blob/043fb46d1a93c77aae656e7c1c64a875d1fc6a0a/action.yml) | v7.0.1 | `043fb46d1a93c77aae656e7c1c64a875d1fc6a0a` |
| [download-artifact](https://github.com/actions/download-artifact/blob/3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c/action.yml) | v8.0.1 | `3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c` |

The existing golangci-lint action commit `1e7e51e771db61008b38414a730f564565cf7c20` also declares Node.js 24.
Its pin and the selected linter version remain unchanged.

The pin change was produced by the managed command, then the generator was rebuilt and the Run regenerated:

```sh
go run ./cmd/nkdctl actions bump \
  --track checkout=v7 --track setup-go=v7 \
  --track upload-artifact=v7 --track download-artifact=v8
```

Handwritten repository CI uses the same checkout/setup-go releases. No runtime-forcing or deprecation opt-out
variable is set. The upload action's default remains archived transport; the optional direct-file mode is not
selected. The download action retains exact-name/pattern selection, merged extraction and bounded concurrent
downloads, preserving the carrier contract.

## Hosted validation

The [generated Run `34423934362`](https://github.com/njhensley/nke-recipes/actions/runs/34423934362)
passed all nine jobs at commit `31fe79c008cbcd7fb213a92be3bfbe39735c0a02`, including private source fetch,
artifact transport, publication, real keyless signing, publication verification and lifecycle finalization.
Its downloaded Run record reports attempt `34423934362-1`, status `complete` and lane `pipeline-attested`.
The content subject remains `sha256:3f820f8adb8104f32906fe64af6aa645cecfa13c5ce904b462fca6ffad355d42`.

The [repository CI Run `34423919628`](https://github.com/njhensley/nke-recipes/actions/runs/34423919628)
also passed. Inspection of the GitHub check-run annotations returned zero annotations for each of the nine
generated jobs and the repository CI job: no Node.js 20 warnings remain in these completed runs.

Before pushing, `make check`, workflow actionlint/yamllint, byte-exact generated-bundle checking and Linux
pipeline/signing race checks passed. The hosted run separately proves the upgraded action versions work
with the existing carrier transport and signing path. This evidence does not establish the stronger
`qualified` lane.
