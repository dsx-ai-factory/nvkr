// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"bytes"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestContainedArchiveKeepsActualRootPathsAndImmediateCarrier(t *testing.T) {
	for _, kind := range []string{"debian-archive", "container", "filesystem"} {
		t.Run(kind, func(t *testing.T) {
			pin := "sha256:" + strings.Repeat("a", 64)
			actual := []DiskFile{{Path: "packages/cache.deb/usr/bin/tool", Kind: "regular", Digest: pin, Size: 7},
				{Path: "var/cache/cache.deb", Kind: "regular", Digest: pin, Size: 42}}
			root := &FilesystemInspection{files: append([]DiskFile(nil), actual...)}
			nested := &FilesystemInspection{components: withDiskContentClass([]Component{{Type: "library", Name: "contained-package", Version: "1"}}, kind),
				files: []DiskFile{{Path: "usr/bin/tool", Kind: "regular", Digest: pin, Size: 7}}}
			appendContainedArchive(root, nested, "var/cache/cache.deb", pin)
			appendContainedArchive(root, nested, "var/cache/second.deb", pin)
			if !reflect.DeepEqual(root.Files(), actual) || len(root.Components()) != 2 {
				t.Fatal("contained discovery changed actual root file paths or lost repeated carriers")
			}
			for index, component := range root.Components() {
				carrier := "var/cache/cache.deb"
				if index == 1 {
					carrier = "var/cache/second.deb"
				}
				if len(component.Properties) != 2 || component.Properties[0] != (Property{Name: "nvkr:containedIn", Value: carrier + "@" + pin}) {
					t.Fatalf("immediate exact carrier absent: %+v", component)
				}
				if class, err := diskComponentClass(component); err != nil || class != kind {
					t.Fatalf("containment changed observation class: %q %v", class, err)
				}
			}
			if len(nested.components[0].Properties) != 1 || nested.files[0].Path != "usr/bin/tool" {
				t.Fatal("root projection mutated the nested observation")
			}
			// An already observed inner carrier is not overwritten with an
			// invented chain through the newly inspected outer archive.
			deeper := &FilesystemInspection{components: root.Components()[:1]}
			outer := &FilesystemInspection{}
			appendContainedArchive(outer, deeper, "outer.tar.gz", "sha256:"+strings.Repeat("b", 64))
			if !reflect.DeepEqual(outer.Components(), deeper.Components()) {
				t.Fatal("nested carrier identity was overwritten")
			}
		})
	}
}

func TestInstallerMemberMergeRetainsImmediateSquashFSCarrier(t *testing.T) {
	raw := helmTestArchive(t, map[string]string{"casper/base.squashfs": "controlled base", "casper/additional.squashfs": "controlled additional"})
	archive, err := os.CreateTemp(t.TempDir(), "installer")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.Write(raw); err != nil {
		t.Fatal(err)
	}
	count := 0
	inspection, err := inspectInstallerMembersWith(t.Context(), archive, func(member inspectionArchive) (*FilesystemInspection, error) {
		count++
		var payload bytes.Buffer
		if _, err := member.WriteToContext(t.Context(), &payload); err != nil {
			return nil, err
		}
		return &FilesystemInspection{components: withDiskContentClass([]Component{{Type: "library", Name: payload.String(), Version: "1"}}, ClassFilesystem),
			files: []DiskFile{{Path: "usr/bin/tool", Kind: "regular", Digest: member.Digest(), Size: member.Size()}}}, nil
	})
	if err != nil || count != 2 || len(inspection.components) != 2 || len(inspection.files) != 0 {
		t.Fatalf("installer merge lost components or invented installed paths: %v", err)
	}
	want := map[string]string{"controlled base": "casper/base.squashfs@" + rawDigest([]byte("controlled base")), "controlled additional": "casper/additional.squashfs@" + rawDigest([]byte("controlled additional"))}
	for _, component := range inspection.components {
		if !slices.Contains(component.Properties, Property{Name: "nvkr:containedIn", Value: want[component.Name]}) {
			t.Fatalf("installer member lost exact carrier: %+v", component)
		}
	}
}
