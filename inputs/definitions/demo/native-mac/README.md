# Native Mac demo inputs

These Definitions select Apple Silicon host tools, the Linux site runtime companion and local runtime images. All native source producers, executable acquisition pins are owned once by the [shared native tool catalog](../../../catalogs/tools/native/README.md). The acquired-only [Mac tools selection](../native-mac-tools/README.md) uses it too.

| Definition | Purpose |
| --- | --- |
| `definition-tools.json` | Host bundle and site runtime companion |
| `definition-runtime-images.json` | Existing pinned runtime and CAPI authentication images for local import |

```sh
bin/nvke-demo tools --repo "$PWD" --nvkr "$PWD/bin/nvkr" \
  --attempt native-tools-001 --out "$PWD/.nvkr/native-tools-001" --jobs 1
```

Each attempt and destination must be fresh. The CLI delegates build, observation, bundle verification and extraction to NVKR. CGO-free Darwin payloads use the explicit pinned Linux ARM64 build container. The CGO-dependent simulator uses the locally measured Go SDK and C compiler through NVKR's native-Go preparation owner. This remains an unqualified local diagnostic. Guest startup receives Linux companions; NVKE bootstrap/deployment commands run on the Mac. Nothing is installed into system directories or sudo policy.

The [native operational guide](../../../../docs/local-nvke-demo.md#native-linux-and-mac-path) describes the site-plus-two-tenant topology and command inputs. NVIDIA OVN acceptance, full native Linux parity, a fresh complete build and qualification remain unverified. QEMU, firmware and ISO tools remain separate host prerequisites.
