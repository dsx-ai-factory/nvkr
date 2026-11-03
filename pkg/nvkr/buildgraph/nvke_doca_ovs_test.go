// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const nodeImageCatalogs = "../../../inputs/catalogs/node-images"

func loadNodeImageCatalog(t *testing.T, name string) *catalog.Catalog {
	t.Helper()
	loaded, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(nodeImageCatalogs, name)))
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func loadNodeImageOverlay(t *testing.T, name string) catalog.Overlay {
	t.Helper()
	overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join(nodeImageCatalogs, "variants", name))
	if err != nil {
		t.Fatal(err)
	}
	return overlay
}

// The release and diagnostic catalogs mirror one DOCA OVS recipe; the Ubuntu
// 24 overlay differs from it only in the Ubuntu series it stages.
func TestDOCAOVSRecipesMirrorExceptSeries(t *testing.T) {
	release := loadNodeImageCatalog(t, "release")
	diagnostic := loadNodeImageCatalog(t, "diagnostic")
	if !reflect.DeepEqual(diagnostic.Components["nvke-doca-ovs"], release.Components["nvke-doca-ovs"]) {
		t.Fatal("diagnostic DOCA OVS recipe diverged from the release recipe")
	}
	if !reflect.DeepEqual(diagnostic.Sources["nvke-doca-ovs-source"], release.Sources["nvke-doca-ovs-source"]) {
		t.Fatal("diagnostic DOCA OVS source diverged from the release source")
	}
	// Ubuntu 24 images stack their overlay on the native toolchains; compare
	// the two effective recipes.
	nativeOverlay := loadNodeImageOverlay(t, "native-toolchains.yaml")
	native, err := catalog.ApplyStack(release, nativeOverlay)
	if err != nil {
		t.Fatal(err)
	}
	ubuntu24, err := catalog.ApplyStack(release, nativeOverlay, loadNodeImageOverlay(t, "ubuntu24.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := native.Catalog.Components["nvke-doca-ovs"]
	want.VendorCommand = slices.Clone(want.VendorCommand)
	vendor := want.VendorCommand[len(want.VendorCommand)-1]
	if strings.Count(vendor, "UBUNTU_SERIES=26.04") != 1 {
		t.Fatal("release DOCA OVS recipe must stage exactly one Ubuntu 26.04 series")
	}
	want.VendorCommand[len(want.VendorCommand)-1] = strings.Replace(vendor, "UBUNTU_SERIES=26.04", "UBUNTU_SERIES=24.04", 1)
	got := ubuntu24.Catalog.Components["nvke-doca-ovs"]
	if got.BuilderType == "" {
		// An overlay replacement leaves the default builder implicit.
		got.BuilderType = catalog.BuilderOCIHermetic
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("Ubuntu 24 DOCA OVS recipe must differ from the release recipe only in its staged series")
	}
}

// The native toolchain overlay moves DOCA OVS acquisition and packaging onto
// the measured AMD64 toolchain and leaves ARM64 on its existing pin.
func TestDOCAOVSUsesNativeAMD64Toolchain(t *testing.T) {
	release := loadNodeImageCatalog(t, "release")
	overlay := loadNodeImageOverlay(t, "native-toolchains.yaml")
	var substitution *catalog.ToolchainSubstitution
	for i := range overlay.ToolchainSubstitutions {
		if overlay.ToolchainSubstitutions[i].Arch == "amd64" && slices.Contains(overlay.ToolchainSubstitutions[i].Components, "nvke-doca-ovs") {
			substitution = &overlay.ToolchainSubstitutions[i]
		}
	}
	if substitution == nil {
		t.Fatal("native toolchains do not substitute the DOCA OVS AMD64 toolchain")
	}
	base := release.Components["nvke-doca-ovs"].ToolchainImages
	if base["amd64"] != substitution.From {
		t.Fatalf("release DOCA OVS AMD64 toolchain %q is not the substituted pin", base["amd64"])
	}
	stack, err := catalog.ApplyStack(release, overlay)
	if err != nil {
		t.Fatal(err)
	}
	images := stack.Catalog.Components["nvke-doca-ovs"].ToolchainImages
	if images["amd64"] != substitution.To || images["arm64"] != base["arm64"] {
		t.Fatalf("native DOCA OVS toolchains = %v", images)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			graph := compileDOCAOVS(t, stack.Catalog, arch)
			fetched, built := false, false
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindFetch && strings.Contains(strings.Join(node.VendorCmd, "\n"), "doca-ovs debs") {
					fetched = true
					if node.VendorImage != images[arch] {
						t.Fatalf("DOCA OVS acquisition runs in %q, want %q", node.VendorImage, images[arch])
					}
				}
				if node.Kind == ir.KindBuild && node.Component == "nvke-doca-ovs" {
					built = true
					if node.Toolchain != images[arch] {
						t.Fatalf("DOCA OVS packaging runs in %q, want %q", node.Toolchain, images[arch])
					}
				}
			}
			if !fetched || !built {
				t.Fatal("DOCA OVS acquisition or packaging missing from graph")
			}
		})
	}
}

func compileDOCAOVS(t *testing.T, selected *catalog.Catalog, arch string) *ir.Graph {
	t.Helper()
	resolved, err := buildgraph.Resolve(selected, buildgraph.Selection{Platform: "linux/" + arch, Components: []string{"nvke-doca-ovs"}})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

// The DOCA OVS archive wrapper runs against fixture debs. A stub dpkg-deb
// reports each fixture's control fields; tar is the host's GNU tar.
func TestDOCAOVSArchiveWrapperExecutesWithFixtures(t *testing.T) {
	gnuTar := gnuTarPath(t)
	stubs := t.TempDir()
	// The stub logs each control-field read so a case proves which guard stopped it.
	if err := os.WriteFile(filepath.Join(stubs, "dpkg-deb"), []byte("#!/bin/sh\n[ \"$1\" = --field ] || exit 2\nprintf '%s\\n' \"$2\" >> \"$NVKR_DOCA_FIELD_LOG\"\nsed -n \"s/^$3: //p\" \"$2\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gnuTar, filepath.Join(stubs, "tar")); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"release", "diagnostic"} {
		for _, arch := range []string{"amd64", "arm64"} {
			graph := compileDOCAOVS(t, loadNodeImageCatalog(t, directory), arch)
			var command []string
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindBuild && node.Component == "nvke-doca-ovs" {
					command = node.Command
				}
			}
			if len(command) != 3 || command[0] != "bash" || command[1] != "-ec" {
				t.Fatalf("%s/%s DOCA OVS packaging command = %q", directory, arch, command)
			}
			// The fixture relocates exactly the working directory and two output paths.
			if got := strings.Count(command[2], "/src/"); got != 3 {
				t.Fatalf("%s/%s DOCA OVS packaging command has %d /src/ paths, want 3", directory, arch, got)
			}
			other := map[string]string{"amd64": "arm64", "arm64": "amd64"}[arch]
			for _, tc := range []struct {
				name  string
				edit  func(debs map[string]string)
				valid bool
				reads int // control fields read before acceptance or rejection
			}{
				{"valid", func(map[string]string) {}, true, 2},
				{"seven packages", func(debs map[string]string) { delete(debs, "doca-ovs-fixture-7_1.0_"+arch+".deb") }, false, 0},
				{"nine packages", func(debs map[string]string) {
					debs["doca-ovs-fixture-9_1.0_"+arch+".deb"] = "Package: doca-ovs-fixture-9\n"
				}, false, 0},
				{"switch for other architecture", func(debs map[string]string) {
					delete(debs, "doca-openvswitch-switch_1.0_"+arch+".deb")
					debs["doca-openvswitch-switch_1.0_"+other+".deb"] = "Package: doca-openvswitch-switch\nArchitecture: " + other + "\n"
				}, false, 0},
				{"two switch packages", func(debs map[string]string) {
					delete(debs, "doca-ovs-fixture-7_1.0_"+arch+".deb")
					debs["doca-openvswitch-switch_2.0_"+arch+".deb"] = "Package: doca-openvswitch-switch\nArchitecture: " + arch + "\n"
				}, false, 0},
				{"switch is a directory", func(debs map[string]string) {
					debs["doca-openvswitch-switch_1.0_"+arch+".deb"] = docaFixtureDirectory
				}, false, 0},
				{"switch package field", func(debs map[string]string) {
					debs["doca-openvswitch-switch_1.0_"+arch+".deb"] = "Package: doca-openvswitch-common\nArchitecture: " + arch + "\n"
				}, false, 1},
				{"switch architecture field", func(debs map[string]string) {
					debs["doca-openvswitch-switch_1.0_"+arch+".deb"] = "Package: doca-openvswitch-switch\nArchitecture: " + other + "\n"
				}, false, 2},
			} {
				t.Run(directory+"/"+arch+"/"+tc.name, func(t *testing.T) {
					debs := map[string]string{"doca-openvswitch-switch_1.0_" + arch + ".deb": "Package: doca-openvswitch-switch\nArchitecture: " + arch + "\n"}
					for i := 1; i < 8; i++ {
						debs[fmt.Sprintf("doca-ovs-fixture-%d_1.0_%s.deb", i, arch)] = fmt.Sprintf("Package: doca-ovs-fixture-%d\n", i)
					}
					tc.edit(debs)
					root := filepath.Join(t.TempDir(), "source with spaces")
					staged := filepath.Join(root, "os-image", "packages", "doca-ovs", "nvkr-debs")
					if err := os.MkdirAll(staged, 0o700); err != nil {
						t.Fatal(err)
					}
					for name, control := range debs {
						var err error
						if control == docaFixtureDirectory {
							err = os.Mkdir(filepath.Join(staged, name), 0o700)
						} else {
							err = os.WriteFile(filepath.Join(staged, name), []byte(control), 0o600)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					// Only the absolute source/output root moves into this fixture.
					script := strings.ReplaceAll(command[2], "/src/", `"${NVKR_DOCA_FIXTURE_ROOT}/"`)
					cmd := exec.CommandContext(t.Context(), command[0], command[1], script)
					fieldLog := filepath.Join(t.TempDir(), "reads")
					// A minimal environment keeps host shell, tar and locale settings out.
					cmd.Env = []string{"NVKR_DOCA_FIELD_LOG=" + fieldLog, "NVKR_DOCA_FIXTURE_ROOT=" + root, "SOURCE_DATE_EPOCH=1793236039",
						"LC_ALL=C", "HOME=" + t.TempDir(), "PATH=" + stubs + string(os.PathListSeparator) + os.Getenv("PATH")}
					output, err := cmd.CombinedOutput()
					archive := filepath.Join(root, "nvkr-output", "nvke-doca-ovs.tar")
					reads, readErr := os.ReadFile(fieldLog)
					if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
						t.Fatal(readErr)
					}
					if got := strings.Count(string(reads), "\n"); got != tc.reads {
						t.Fatalf("wrapper read %d control fields, want %d: %s", got, tc.reads, output)
					}
					if !tc.valid {
						if err == nil {
							t.Fatalf("wrapper accepted invalid fixture: %s", output)
						}
						if _, statErr := os.Stat(archive); !errors.Is(statErr, os.ErrNotExist) {
							t.Fatal("rejected fixture left an archive", statErr)
						}
						return
					}
					if err != nil {
						t.Fatalf("wrapper rejected valid fixture: %v: %s", err, output)
					}
					members := readDOCAOVSArchive(t, archive)
					if !maps.Equal(members, debs) {
						t.Fatalf("archive members = %v, want exactly the staged debs", slices.Sorted(maps.Keys(members)))
					}
				})
			}
		}
	}
}

// docaFixtureDirectory stages a directory, not a file, under that deb name.
const docaFixtureDirectory = "<directory>"

func gnuTarPath(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"tar", "gtar"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		version, err := exec.CommandContext(t.Context(), path, "--version").Output()
		if err == nil && strings.Contains(string(version), "GNU tar") {
			return path
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatal("the DOCA OVS archive wrapper requires GNU tar in CI")
	}
	t.Skip("the DOCA OVS archive wrapper requires GNU tar; Linux CI provides it")
	return ""
}

// readDOCAOVSArchive returns the archive's member bodies by name and checks the
// wrapper's sorted, reproducible ustar headers.
func readDOCAOVSArchive(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	members := map[string]string{}
	previous := ""
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return members
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Format != tar.FormatUSTAR || header.Uid != 0 || header.Gid != 0 || header.ModTime.Unix() != 1793236039 ||
			header.Typeflag != tar.TypeReg || strings.Contains(header.Name, "/") || header.Name <= previous {
			t.Fatalf("non-reproducible archive member %+v", header)
		}
		previous = header.Name
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		members[header.Name] = string(body)
	}
}
