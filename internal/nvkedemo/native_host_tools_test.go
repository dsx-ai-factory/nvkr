// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

func TestNativeHostToolViewPreservesOriginalBundleAndBytes(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		aliases        bool
	}{
		{"Mac", "darwin/arm64", false},
		{"Linux shared catalog", "linux/arm64", true},
		{"retained Linux bundle", "linux/arm64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, publication, originalNames := hostToolViewFixture(t, tc.platform, tc.aliases)
			bundle, _, err := delivery.InspectBinaryBundle(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(privateTestDir(t), "tools")
			if err := extractNativeHostTools(t.Context(), bundle, publication, out, tc.platform); err != nil {
				t.Fatal(err)
			}
			_, _, _, members, err := nativeToolSelection(tc.platform)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyToolMembers(out, members); err != nil {
				t.Fatal(err)
			}
			for member, original := range originalNames {
				mode := os.FileMode(0755)
				if member == "nvke-installer-sources" {
					mode = 0644
				}
				for _, path := range []string{filepath.Join(out+"-original", original), filepath.Join(out, member)} {
					info, err := os.Lstat(path)
					if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
						t.Fatalf("%s changed delivered mode: %v, %v", path, info, err)
					}
				}
				from, err := DigestFile(t.Context(), filepath.Join(out+"-original", original))
				if err != nil {
					t.Fatal(err)
				}
				to, err := DigestFile(t.Context(), filepath.Join(out, member))
				if err != nil || from != to {
					t.Fatalf("%s changed bytes: %v", member, err)
				}
			}
			if _, _, err := delivery.InspectBinaryBundle(t.Context(), source); err != nil {
				t.Fatal("original bundle changed", err)
			}
			if err := extractNativeHostTools(t.Context(), bundle, publication, out, tc.platform); err == nil {
				t.Fatal("existing tool view was overwritten")
			}
		})
	}
}

func TestNativeHostToolViewRejectsBeforeExtraction(t *testing.T) {
	for _, scenario := range []string{"wrong platform", "existing view", "existing original", "canceled", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			source, publication, _ := hostToolViewFixture(t, "darwin/arm64", false)
			bundle, _, err := delivery.InspectBinaryBundle(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(privateTestDir(t), "tools")
			platform := "darwin/arm64"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var existing string
			switch scenario {
			case "wrong platform":
				platform = "linux/arm64"
			case "existing view":
				existing = out
			case "existing original":
				existing = out + "-original"
			case "canceled":
				cancel()
			case "unsupported":
				platform = "windows/arm64"
			}
			if existing != "" {
				if err := os.Mkdir(existing, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(existing, "sentinel"), []byte("unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = extractNativeHostTools(ctx, bundle, publication, out, platform)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeVerifyFailed {
				t.Fatalf("uncoded rejection: %v", err)
			}
			if scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost")
			}
			for _, path := range []string{out, out + "-original"} {
				if path == existing {
					continue
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("rejected tools produced an extraction", path)
				}
			}
			if existing != "" {
				raw, err := os.ReadFile(filepath.Join(existing, "sentinel"))
				if err != nil || string(raw) != "unchanged" {
					t.Fatal("existing tools changed")
				}
			}
		})
	}
}

func TestNativeHostToolAliasesRejectUnexpectedAndCollidingMembers(t *testing.T) {
	for _, scenario := range []string{"missing", "extra", "collision", "Mac with Linux alias"} {
		t.Run(scenario, func(t *testing.T) {
			platform := "linux/arm64"
			members := []string{"helm", "kind"}
			bundle := delivery.BinaryBundle{OS: "linux", Arch: "arm64", Entries: []delivery.BinaryEntry{{Name: "helm-linux"}, {Name: "kind-linux"}}}
			switch scenario {
			case "missing":
				bundle.Entries = bundle.Entries[:1]
			case "extra":
				bundle.Entries[1].Name = "unexpected"
			case "collision":
				bundle.Entries[1].Name = "helm"
			case "Mac with Linux alias":
				platform = "darwin/arm64"
				bundle.OS = "darwin"
			}
			if _, err := nativeHostToolBindings(bundle, platform, members); err == nil {
				t.Fatal("unexpected tool mapping admitted")
			}
		})
	}
}

func hostToolViewFixture(t *testing.T, platform string, aliases bool) (string, string, map[string]string) {
	t.Helper()
	root := privateTestDir(t)
	_, _, bundleName, members, err := nativeToolSelection(platform)
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	var files []delivery.BinaryFileInput
	for _, member := range members {
		name := member
		if aliases {
			switch member {
			case "clusterctl", "helm", "kind", "kubectl", "oras":
				name += "-linux"
			}
		}
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("exact "+member), 0700); err != nil {
			t.Fatal(err)
		}
		bindings[member] = name
		mode := uint32(0755)
		if member == "nvke-installer-sources" {
			mode = 0644
		}
		files = append(files, delivery.BinaryFileInput{Name: name, Path: path, InstallPath: "/usr/local/bin/" + name, Mode: mode})
	}
	osName, arch, _ := strings.Cut(platform, "/")
	source, publication := filepath.Join(root, "bundle"), filepath.Join(root, "publication")
	built, err := delivery.BuildBinaryBundle(t.Context(), source, delivery.BinaryBundleRequest{OS: osName, Arch: arch, Purpose: bundleName, Epoch: 1, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	_, err = delivery.BuildPublicationRoot(t.Context(), publication, delivery.PublicationRequest{Objects: []delivery.ObjectInput{{Name: bundleName, Kind: "binary-bundle", Path: source, ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: built.SemanticDigest, BlobDigest: built.RootDigest, OS: osName, Arch: arch, Layout: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return source, publication, bindings
}

func TestNativeHostToolBindingsRequireDeclaredMemberModes(t *testing.T) {
	for _, platform := range []string{"linux/arm64", "darwin/arm64"} {
		for _, scenario := range []string{"correct", "executable archive", "nonexecutable command", "writable archive"} {
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				_, _, _, members, err := nativeToolSelection(platform)
				if err != nil {
					t.Fatal(err)
				}
				osName, arch, _ := strings.Cut(platform, "/")
				bundle := delivery.BinaryBundle{OS: osName, Arch: arch}
				for _, member := range members {
					mode := uint32(0755)
					if member == "nvke-installer-sources" {
						mode = 0644
						switch scenario {
						case "executable archive":
							mode = 0755
						case "writable archive":
							mode = 0666
						}
					} else if member == "nvkectl" && scenario == "nonexecutable command" {
						mode = 0644
					}
					bundle.Entries = append(bundle.Entries, delivery.BinaryEntry{Name: member, Mode: mode})
				}
				_, err = nativeHostToolBindings(bundle, platform, members)
				if (err == nil) != (scenario == "correct") {
					t.Fatalf("member mode admission: %v", err)
				}
			})
		}
	}
}

func TestNativeToolAdmissionBindsExactCarrierIndex(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same carrier", true: "coherent carrier changed after Run binding"}[changed], func(t *testing.T) {
			source, _, _ := hostToolViewFixture(t, "linux/arm64", false)
			path := filepath.Join(source, "index.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var index v1.IndexManifest
			if err := json.Unmarshal(raw, &index); err != nil {
				t.Fatal(err)
			}
			admitted, err := diagnosticIndexDigest(t.Context(), source, index.Manifests[0].Digest.String())
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				// The child and semantic bundle remain identical. Exact index
				// bytes must still be the ones admitted against the completed Run.
				if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				if _, _, err := delivery.InspectBinaryBundle(t.Context(), source); err != nil {
					t.Fatal("replacement is not a coherent bundle", err)
				}
			}
			_, result, err := inspectAdmittedBinaryBundle(t.Context(), source, admitted)
			if (err == nil) == changed || !changed && result.RootDigest != admitted || changed && result.RootDigest != "" {
				t.Fatalf("exact carrier admission: result=%+v err=%v", result, err)
			}
		})
	}
}

// These are real independently built bundle layouts, not fabricated receipt or
// validator descriptions. Delivery owns each publication copy and extraction.
func TestNativeHostToolViewUsesCapturedBytes(t *testing.T) {
	for _, scenario := range []string{"source changed before capture", "source moved after capture", "different captured bundle"} {
		t.Run(scenario, func(t *testing.T) {
			const platform = "linux/arm64"
			source, publication, _ := hostToolViewFixture(t, platform, true)
			bundle, inspected, err := delivery.InspectBinaryBundle(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			captured := filepath.Join(filepath.Dir(publication), "captured")
			request := delivery.PublicationRequest{Objects: []delivery.ObjectInput{{Name: bundle.Purpose, Kind: "binary-bundle", Path: source,
				ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: inspected.SemanticDigest, BlobDigest: inspected.RootDigest, Layout: true}}}
			if scenario == "source changed before capture" {
				path := filepath.Join(source, "index.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
				if _, _, err := delivery.InspectBinaryBundle(t.Context(), source); err != nil {
					t.Fatal("replacement must remain a coherent bundle", err)
				}
			}
			_, err = delivery.BuildPublicationRoot(t.Context(), captured, request)
			if scenario == "source changed before capture" {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeVerifyFailed {
					t.Fatal("publication accepted a substituted original carrier", err)
				}
				if _, statErr := os.Lstat(captured); !os.IsNotExist(statErr) {
					t.Fatal("rejected carrier committed a publication", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "source moved after capture" {
				if err := os.Rename(source, source+"-moved"); err != nil {
					t.Fatal(err)
				}
			} else {
				_, captured, _ = hostToolViewFixture(t, platform, false)
			}
			out := filepath.Join(privateTestDir(t), "tools")
			err = extractNativeHostTools(t.Context(), bundle, captured, out, platform)
			if scenario == "source moved after capture" {
				if err != nil {
					t.Fatal("captured bytes depended on the original source path", err)
				}
				return
			}
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeVerifyFailed {
				t.Fatal("different captured bundle escaped the original binding", err)
			}
			if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
				t.Fatal("mismatched bundle produced the final tool view", statErr)
			}
		})
	}
}
