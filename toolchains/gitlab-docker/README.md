# Real GitLab integration infrastructure

`pins.json` pins official multi-platform images by version and index digest. The
integration test checks each resolved image matches its native Linux daemon.
The Environment selects the pinned official Go image directly. An owned
read-only tool volume supplies Docker CLI copied from its pinned official image.
The Runner uses an explicit tool path and a same-daemon socket probe. This infrastructure does not replace NVKR catalog build commands.

The pinned GitLab server and Runner are external test tools. `glab` is invoked as
an external executable; its dependencies are not linked into NVKR. Consult the
[development guide](../../DEVELOPMENT.md#test-the-gitlab-adapter-against-a-real-server)
for resources, credential isolation, evidence and cleanup.

Primary image inventories:

- [GitLab CE](https://hub.docker.com/v2/repositories/gitlab/gitlab-ce/tags/19.4.8-ce.0)
- [GitLab Runner](https://hub.docker.com/v2/repositories/gitlab/gitlab-runner/tags/alpine-v19.4.0)
- [Runner ARM64 helper](https://hub.docker.com/v2/repositories/gitlab/gitlab-runner-helper/tags/arm64-v19.4.0)
- [Runner AMD64 helper](https://hub.docker.com/v2/repositories/gitlab/gitlab-runner-helper/tags/x86_64-v19.4.0)
- [GitLab CLI](https://hub.docker.com/v2/repositories/gitlab/glab/tags/v1.131.0)
- [Go](https://hub.docker.com/v2/repositories/library/golang/tags/1.27.4)

Refresh version and digest together, inspect both AMD64/ARM64 manifests, and run
the integration test before recording a new provider-acceptance result. The
Docker CLI matches the maintained Tekton integration pin. Runner helper images
are pinned separately for ARM64 and AMD64; refresh those with the Runner version.
