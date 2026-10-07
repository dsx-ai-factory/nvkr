package pipeline

import (
	"errors"
	"slices"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestPreparedPrivateImageRequiresExactLocalDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "ghcr.io/example/tool@" + digest
	for _, test := range []struct {
		name, document string
		missing        bool
		want           e.Code
	}{
		{"present", `["` + ref + `"]`, false, ""},
		{"missing then pulled", `["` + ref + `"]`, true, ""},
		{"substituted", `["ghcr.io/example/tool@sha256:` + strings.Repeat("b", 64) + `"]`, false, e.CodePreflightFailed},
		{"malformed", `{`, false, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls [][]string
			got, err := observeContainerImageWith(t.Context(), ir.Node{ID: "private", Toolchain: ref, Platform: "linux/amd64"}, func(args ...string) ([]byte, error) {
				calls = append(calls, slices.Clone(args))
				if len(calls) == 1 && test.missing {
					return nil, errors.New("image absent")
				}
				if args[0] == "pull" {
					return nil, nil
				}
				return []byte(test.document), nil
			})
			wantCalls := [][]string{{"image", "inspect", "--format", "{{json .RepoDigests}}", ref}}
			if test.missing {
				wantCalls = append(wantCalls, []string{"pull", "--platform", "linux/amd64", ref}, wantCalls[0])
			}
			if !slices.EqualFunc(calls, wantCalls, func(got, want []string) bool { return slices.Equal(got, want) }) {
				t.Fatalf("toolchain lookup/pull argv = %v, want %v", calls, wantCalls)
			}
			if test.want != "" {
				if code(err) != test.want || got != "" {
					t.Fatalf("unexpected identity admission: %q / %v", got, err)
				}
			} else if err != nil || got != digest {
				t.Fatalf("exact prepared digest was rejected: %q / %v", got, err)
			}
		})
	}
}

func TestLocalImageIdentityUsesPortableExactIDProjection(t *testing.T) {
	want := "sha256:" + strings.Repeat("b", 64)
	for _, test := range []struct {
		name, document string
		code           e.Code
	}{
		{"Docker prefixed", `{"Id":"` + want + `"}`, ""},
		{"Podman raw", `{"Id":"` + strings.TrimPrefix(want, "sha256:") + `"}`, ""},
		{"wrong prefixed", `{"Id":"sha256:` + strings.Repeat("c", 64) + `"}`, e.CodePreflightFailed},
		{"wrong raw", `{"Id":"` + strings.Repeat("c", 64) + `"}`, e.CodePreflightFailed},
		{"missing ID", `{}`, e.CodePreflightFailed},
		{"malformed object", `{`, e.CodeEngineCapability},
		{"duplicate ID", `{"Id":"` + want + `","Id":"` + want + `"}`, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			got, err := observeContainerImageWith(t.Context(), ir.Node{ID: "node", Toolchain: want}, func(args ...string) ([]byte, error) {
				calls++
				if !slices.Equal(args, []string{"image", "inspect", "--format", "{{json .}}", want}) {
					t.Fatalf("local exact identity must use the portable object, without pulling: %v", args)
				}
				return []byte(test.document), nil
			})
			if calls != 1 {
				t.Fatalf("wrong admission operation count: %d", calls)
			}
			if test.code == "" {
				if err != nil || got != want {
					t.Fatalf("exact runtime ID rejected: %q / %v", got, err)
				}
			} else if got != "" || code(err) != test.code {
				t.Fatalf("incorrect substituted ID admission: %q / %v", got, err)
			}
		})
	}
}

func TestSourceVendorImageNormalizesOnlyCanonicalLocalIDs(t *testing.T) {
	want := "sha256:" + strings.Repeat("b", 64)
	for _, test := range []struct {
		name, id string
		valid    bool
	}{
		{"Docker prefixed", want, true},
		{"Podman raw", strings.TrimPrefix(want, "sha256:"), true},
		{"short hex", "abcd", false},
		{"wrong algorithm", "sha512:" + strings.Repeat("b", 64), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := sourceVendorRuntimeFixture(t, "linux/arm64")
			r.image = strings.Replace(r.image, want, test.id, 1)
			got, err := sourceVendorImage(t.Context(), r, want, "linux/arm64")
			if test.valid {
				if err != nil || got != want {
					t.Fatalf("canonical native ID projection failed: %q / %v", got, err)
				}
			} else if got != "" || code(err) != e.CodeEngineCapability {
				t.Fatalf("noncanonical ID passed normalization: %q / %v", got, err)
			}
		})
	}
}
