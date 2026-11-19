export GOENV=off GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
build_date="$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)"
out=/src/bin/nvkr-aicr
mkdir -p "$out"
timeout --kill-after=10s 900s go build -mod=vendor -trimpath -buildvcs=false \
  -ldflags "-s -w -buildid= -X github.com/NVIDIA/aicr/pkg/cli.version=$BUILD_VERSION -X github.com/NVIDIA/aicr/pkg/cli.commit=$GIT_VERSION -X github.com/NVIDIA/aicr/pkg/cli.date=$build_date" \
  -o "$out/aicr" ./cmd/aicr
timeout --kill-after=10s 120s "$out/aicr" recipe \
  --service "$1" --accelerator "$2" --platform "$3" \
  --os ubuntu --intent training --output "$out/recipe.yaml"
# Exact-leaf admission: the most specific applied overlay must be this coordinate's own
# leaf, whatever a later AICR pin resolves. The recipe's only appliedOverlays list must be
# a direct child of top-level metadata, and any list line other than a plain item
# rejects the whole list.
leaf="$2-$1-ubuntu-training-$3"
applied="$(awk '
  /^[^[:space:]#]/ { meta = 0 }
  /^metadata:[[:space:]]*$/ { meta = 1; child = -1; next }
  meta && child < 0 && /^[[:space:]]+[^[:space:]#]/ { match($0, /^[[:space:]]+/); child = RLENGTH }
  /^[[:space:]]*appliedOverlays:[[:space:]]*$/ {
    lists++; match($0, /^[[:space:]]*/); inlist = meta && RLENGTH == child; scoped += inlist; next
  }
  inlist && /^[[:space:]]*- [^[:space:]]+[[:space:]]*$/ { last = $2; next }
  inlist && (/^[[:space:]]*-/ || /^[[:space:]]*#/ || /^[[:space:]]*$/) { bad = 1 }
  { inlist = 0 }
  END { if (lists == 1 && scoped == 1 && !bad) print last }
' "$out/recipe.yaml")"
[ "$applied" = "$leaf" ] || { echo "AICR recipe must end in leaf $leaf, got '$applied'" >&2; exit 1; }
# AICR's NVKE guide gates network-operator readiness before the IB pools; Flux cannot.
hooks=()
case "$4" in helm | argocd-helm) hooks=(--readiness-hooks) ;; esac
# NVKE's GPU Operator installs the driver, so the bundle opts in to AICR's DRA eviction:
# Driver Manager deschedules the DRA kubelet plugin before a driver restart.
# Layout output stages in the working directory; the source tree is read-only.
(
  cd "$out"
  timeout --kill-after=10s 120s "$out/aicr" bundle \
    --recipe "$out/recipe.yaml" --deployer "$4" "${hooks[@]}" \
    --dra-eviction-node-label nvidia.com/dra-kubelet-plugin=true \
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
[ "$4" != argocd-helm ] || root="$root/aicr-bundle"
timeout --kill-after=10s 120s "$out/aicr" verify "$root"
cp "$layer" "$out/bundle.tar.gz"
