# Delivery test fixtures

## `aicr-v1.1.2-helm`

An OCI image layout holding the AICR v1.1.2 Helm deployment bundle exactly as AICR pushes it.
The manifest is `sha256:7ecb2e63700525a9052c9ade020d9e054f195f2f4c77478b7130623bf152ee8f`, and the
bundle layer is `sha256:b923757f1f7fa8782e676b33b817723fb51b7ac01ef6c7be2111baa791d7ed1d`.

It was generated on macOS arm64 with the `aicr_1.1.2_darwin_arm64.tar.gz` release asset, after checking it
against `aicr_checksums.txt` from the same release. A throwaway local registry received the push, and
`oras cp` copied it into a layout:

```sh
docker run -d --rm --name aicr-fixture-registry -p 127.0.0.1:55285:5000 registry:2
aicr recipe --service generic --accelerator gb300 --os ubuntu --intent training --output recipe.yaml
aicr bundle --recipe recipe.yaml --deployer helm --plain-http \
  --output oci://127.0.0.1:55285/aicr-bundle:v1.1.2
oras cp --from-plain-http 127.0.0.1:55285/aicr-bundle:v1.1.2 --to-oci-layout layout:v1.1.2
docker stop aicr-fixture-registry
```

`aicr bundle` also writes the rendered directory to `./bundle`, which the fixture doesn't need. The
blob files are byte-for-byte what `oras cp` wrote; only their file modes changed.
