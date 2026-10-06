package pipeline

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestPreparedPrivateImageRequiresExactLocalDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "ghcr.io/example/tool@" + digest
	document := fmt.Sprintf(`{"RepoDigests":[%q],"Os":"linux","Architecture":"amd64"}`, ref)
	for _, test := range []struct {
		name, document string
		missing        bool
		want           e.Code
	}{
		{"present", document, false, ""},
		{"missing then pulled", document, true, ""},
		{"substituted", strings.Replace(document, digest, "sha256:"+strings.Repeat("b", 64), 1), false, e.CodePreflightFailed},
		{"pulled substituted", strings.Replace(document, digest, "sha256:"+strings.Repeat("b", 64), 1), true, e.CodePreflightFailed},
		{"malformed", `{`, false, e.CodeEngineCapability},
		{"pulled malformed", `{`, true, e.CodeEngineCapability},
		{"duplicate digest field", strings.Replace(document, `"RepoDigests":`, `"RepoDigests":[],"RepoDigests":`, 1), false, e.CodeEngineCapability},
		{"duplicate OS field", strings.Replace(document, `"Os":`, `"Os":"windows","Os":`, 1), false, e.CodeEngineCapability},
		{"pulled duplicate architecture", strings.Replace(document, `"Architecture":`, `"Architecture":"arm64","Architecture":`, 1), true, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, pulls := 0, 0
			got, err := observeContainerImageWith(t.Context(), ir.Node{ID: "private", Toolchain: ref, Platform: "linux/amd64"}, func(args ...string) ([]byte, error) {
				calls++
				if calls == 1 && test.missing {
					return nil, errors.New("image absent")
				}
				if args[0] == "pull" {
					pulls++
					return nil, nil
				}
				return []byte(test.document), nil
			})
			if pulls != map[bool]int{false: 0, true: 1}[test.missing] {
				t.Fatal("present private image unexpectedly needed registry credentials")
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

func TestPreparedImageRequiresSelectedToolchainPlatform(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "ghcr.io/example/tool@" + digest
	for _, test := range []struct {
		name, builder, platform, host, selected, observedOS, observedArch string
		want                                                              e.Code
	}{
		{"OCI amd64", ir.BuilderOCIHermetic, "linux/amd64", "", "linux/amd64", "linux", "amd64", ""},
		{"OCI arm64", ir.BuilderOCIHermetic, "linux/arm64", "", "linux/arm64", "linux", "arm64", ""},
		{"OCI wrong OS", ir.BuilderOCIHermetic, "linux/amd64", "", "linux/amd64", "windows", "amd64", e.CodePreflightFailed},
		{"OCI wrong arch", ir.BuilderOCIHermetic, "linux/amd64", "", "linux/amd64", "linux", "arm64", e.CodePreflightFailed},
		{"OCI ignores VM host field", ir.BuilderOCIHermetic, "linux/amd64", "linux/arm64", "linux/amd64", "linux", "amd64", ""},
		{"VM arm guest amd host", ir.BuilderVMImage, "linux/arm64", "linux/amd64", "linux/amd64", "linux", "amd64", ""},
		{"VM rejects arm guest image", ir.BuilderVMImage, "linux/arm64", "linux/amd64", "linux/amd64", "linux", "arm64", e.CodePreflightFailed},
		{"VM amd guest arm host", ir.BuilderVMImage, "linux/amd64", "linux/arm64", "linux/arm64", "linux", "arm64", ""},
		{"VM rejects amd guest image", ir.BuilderVMImage, "linux/amd64", "linux/arm64", "linux/arm64", "linux", "amd64", e.CodePreflightFailed},
		{"VM wrong OS", ir.BuilderVMImage, "linux/arm64", "linux/amd64", "linux/amd64", "windows", "amd64", e.CodePreflightFailed},
		{"missing observed OS", ir.BuilderOCIHermetic, "linux/amd64", "", "linux/amd64", "", "amd64", e.CodePreflightFailed},
		{"missing observed arch", ir.BuilderOCIHermetic, "linux/amd64", "", "linux/amd64", "linux", "", e.CodePreflightFailed},
		{"missing VM host", ir.BuilderVMImage, "linux/amd64", "", "", "linux", "amd64", e.CodeInvalidCatalog},
		{"invalid selected platform", ir.BuilderOCIHermetic, "linux/any", "", "linux/any", "linux", "amd64", e.CodeInvalidCatalog},
	} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pull=%t", test.name, missing), func(t *testing.T) {
				calls, pulls := 0, 0
				node := ir.Node{ID: "toolchain", Toolchain: ref, BuilderType: test.builder, Platform: test.platform, ToolchainPlatform: test.host}
				document := fmt.Sprintf(`{"RepoDigests":[%q],"Os":%q,"Architecture":%q}`, ref, test.observedOS, test.observedArch)
				got, err := observeContainerImageWith(t.Context(), node, func(args ...string) ([]byte, error) {
					calls++
					if args[0] == "pull" {
						pulls++
						if !slices.Equal(args, []string{"pull", "--platform", test.selected, ref}) {
							t.Fatalf("pull did not select the toolchain host: %v", args)
						}
						return nil, nil
					}
					if !slices.Equal(args, []string{"image", "inspect", "--format", "{{json .}}", ref}) {
						t.Fatalf("inspection did not request portable full metadata: %v", args)
					}
					if calls == 1 && missing {
						return nil, errors.New("image absent")
					}
					return []byte(document), nil
				})
				wantCalls, wantPulls := 1, 0
				if missing {
					wantCalls, wantPulls = 3, 1
				}
				if test.want == e.CodeInvalidCatalog {
					wantCalls, wantPulls = 0, 0
				}
				if calls != wantCalls || pulls != wantPulls {
					t.Fatalf("unexpected materialization: calls=%d pulls=%d, want %d/%d", calls, pulls, wantCalls, wantPulls)
				}
				if test.want != "" {
					if got != "" || code(err) != test.want {
						t.Fatalf("incorrect image admission: %q / %v, want %s", got, err, test.want)
					}
				} else if err != nil || got != digest {
					t.Fatalf("selected host image rejected: %q / %v", got, err)
				}
			})
		}
	}
}

func TestPreparedImageFallbackFailureStopsIdentityObservation(t *testing.T) {
	ref := "ghcr.io/example/tool@sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name      string
		pullFails bool
		wantCalls int
	}{
		{"pull fails", true, 2}, {"post-pull inspection fails", false, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			got, err := observeContainerImageWith(t.Context(), ir.Node{ID: "tool", Toolchain: ref, Platform: "linux/amd64"}, func(args ...string) ([]byte, error) {
				calls++
				if args[0] == "pull" && !test.pullFails {
					return nil, nil
				}
				return nil, errors.New("synthetic runtime failure")
			})
			if got != "" || code(err) != e.CodeEngineCapability || calls != test.wantCalls {
				t.Fatalf("failed materialization continued: %q / %v, calls=%d", got, err, calls)
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
