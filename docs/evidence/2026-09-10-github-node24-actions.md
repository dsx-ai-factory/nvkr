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

Pending dispatch after the complete local gate, workflow linting, exact regeneration and Linux pipeline/signing
race checks. Static validation alone does not prove the upgraded provider actions work or clear annotations.
