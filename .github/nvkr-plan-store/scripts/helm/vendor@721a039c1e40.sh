export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=
pins="$(sha256sum go.mod go.sum)"
timeout --kill-after=10s 540s go mod download
printf '%s\n' "$pins" | sha256sum --quiet -c -
GOPROXY=off timeout --kill-after=10s 60s go mod verify
GOPROXY=off timeout --kill-after=10s 180s go mod vendor
printf '%s\n' "$pins" | sha256sum --quiet -c -
# Reuse PR113's diagnostic Local preparation. These pinned negative-test
# fixture links are not cmd/helm inputs. The held hosted fetch route
# rejects external links BEFORE this command; do not use it for this lane.
test "$(readlink internal/chart/v3/loader/testdata/frobnitz_with_dev_null/null)" = /dev/null
test "$(readlink pkg/chart/v2/loader/testdata/frobnitz_with_dev_null/null)" = /dev/null
test "$(readlink internal/third_party/dep/fs/testdata/symlinks/invalid-symlink)" = /non/existing/file
test "$(readlink internal/third_party/dep/fs/testdata/symlinks/windows-file-symlink)" = C:/Users/ibrahim/go/src/github.com/golang/dep/internal/fs/testdata/test.file
# Docker's vendor container may have a different UID from the checkout
# owner. Trust only this already pinned private source root, not '*'.
git -c safe.directory=/src rm -- internal/chart/v3/loader/testdata/frobnitz_with_dev_null/null \
  pkg/chart/v2/loader/testdata/frobnitz_with_dev_null/null \
  internal/third_party/dep/fs/testdata/symlinks/invalid-symlink \
  internal/third_party/dep/fs/testdata/symlinks/windows-file-symlink
