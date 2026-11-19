export GOENV=off GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
build_date="$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)"
out=/src/bin/nvkr-aicr
mkdir -p "$out"
timeout --kill-after=10s 900s go build -mod=vendor -trimpath -buildvcs=false \
  -ldflags "-s -w -buildid= -X aicr.run/pkg/cli.version=$BUILD_VERSION -X aicr.run/pkg/cli.commit=$GIT_VERSION -X aicr.run/pkg/cli.date=$build_date" \
  -o "$out/aicr" ./cmd/aicr
version_report="$(timeout --kill-after=10s 30s "$out/aicr" --version)"
[ "$version_report" = "aicr version $BUILD_VERSION (commit: $GIT_VERSION, date: $build_date)" ] || { echo "AICR build identity does not match pinned source" >&2; exit 1; }
timeout --kill-after=10s 120s "$out/aicr" recipe \
  --service "$1" --accelerator "$2" \
  --os ubuntu --intent training --output "$out/recipe.yaml"
# OCI output requires an Argo CD repoURL; this literal never resolves, so consumers set their own.
repo=()
[ "$3" != argocd ] || repo=(--repo oci://registry.invalid/aicr-bundle)
# Layout output stages in the working directory; the source tree is read-only.
(
  cd "$out"
  timeout --kill-after=10s 120s "$out/aicr" bundle \
    --recipe "$out/recipe.yaml" --deployer "$3" "${repo[@]}" \
    --output "oci-layout:$out/layout"
)
# Publish AICR's own layer bytes: one manifest holding the config and exactly one layer.
digests() { grep -o '"digest":"sha256:[0-9a-f]\{64\}"' "$1" | cut -d: -f3 | tr -d '"'; }
mapfile -t manifest < <(digests "$out/layout/index.json")
[ "${#manifest[@]}" -eq 1 ] || { echo 'AICR layout must index one manifest' >&2; exit 1; }
mapfile -t blobs < <(digests "$out/layout/blobs/sha256/${manifest[0]}")
[ "${#blobs[@]}" -eq 2 ] || { echo 'AICR manifest must reference one config and one layer' >&2; exit 1; }
layer="$out/layout/blobs/sha256/${blobs[1]}"
printf '%s  %s\n' "${blobs[1]}" "$layer" | sha256sum --quiet -c -
mkdir "$out/verify"
tar -xzf "$layer" -C "$out/verify"
root="$out/verify"
[ "$3" != argocd-helm ] || root="$root/aicr-bundle"
timeout --kill-after=10s 120s "$out/aicr" verify "$root"
cp "$layer" "$out/bundle.tar.gz"
