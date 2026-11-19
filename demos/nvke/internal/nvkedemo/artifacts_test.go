// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"golang.org/x/sys/unix"
)

func TestNativeRunSelectionDelegatesMetadataAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		code nvkrerr.Code
	}{
		{"selected", ""}, {"missing selected", nvkrerr.CodeVerifyFailed}, {"wrong selected kind", nvkrerr.CodeVerifyFailed},
		{"failed", nvkrerr.CodeVerifyFailed}, {"empty", nvkrerr.CodeVerifyFailed},
		{"duplicate expected", nvkrerr.CodeVerifyFailed}, {"duplicate observed", nvkrerr.CodeVerifyFailed},
		{"extra", nvkrerr.CodeVerifyFailed}, {"missing", nvkrerr.CodeVerifyFailed},
		{"wrong build", nvkrerr.CodeVerifyFailed}, {"wrong components", nvkrerr.CodeVerifyFailed},
		{"wrong schema", nvkrerr.CodeVerifyFailed}, {"duplicate JSON", nvkrerr.CodeVerifyFailed},
		{"noncanonical JSON", nvkrerr.CodeVerifyFailed}, {"symlink record", nvkrerr.CodeVerifyFailed}, {"symlink root", nvkrerr.CodeVerifyFailed},
		{"uncaptured", nvkrerr.CodeVerifyFailed}, {"missing original Plan", nvkrerr.CodeVerifyFailed},
		{"wrong Plan", nvkrerr.CodePipelineDrift}, {"wrong attempt", nvkrerr.CodeVerifyFailed},
		{"hosted lane", nvkrerr.CodeGateUnmet}, {"missing content identity", nvkrerr.CodeGateUnmet},
		{"unfinished disk commit", nvkrerr.CodeImmutableViolation}, {"canceled", nvkrerr.CodeTimeout}, {"nil context", nvkrerr.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(privateTestDir(t), "fixture")
			_, run := capturedArtifactRunFixture(t, root, []lifecycle.DefinitionOutput{
				{Name: "other", Platform: "linux/arm64", Format: "executable", Components: []string{"tool"}},
				{Name: "tools", Platform: "linux/arm64", Format: "executable", Components: []string{"tool"}},
			}, nil)
			selected, kind, ctx := "tools", "executable", t.Context()
			switch tc.name {
			case "missing selected":
				selected = "absent"
			case "wrong selected kind":
				kind = "oci"
			case "failed":
				run.Status = lifecycle.RunFailed
			case "empty":
				run.Expected, run.Observed = nil, nil
			case "duplicate expected":
				run.Expected[0] = run.Expected[1]
			case "duplicate observed":
				run.Observed[0] = run.Observed[1]
			case "extra":
				extra := run.Observed[0]
				extra.Name = "extra"
				run.Observed = append(run.Observed, extra)
			case "missing":
				run.Observed = run.Observed[:1]
			case "wrong build":
				run.Observed[0].BuildDigest = "sha256:" + strings.Repeat("c", 64)
			case "wrong components":
				run.Observed[0].Components = []string{"different"}
			case "wrong schema":
				run.SchemaVersion = "nvkr.dev/run/v2"
			case "wrong Plan":
				run.PlanDigest = "sha256:" + strings.Repeat("9", 64)
			case "wrong attempt":
				run.Attempt = "other"
			case "hosted lane":
				run.Lane = lifecycle.LanePipeline
			case "missing content identity":
				run.ContentSubject = ""
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil context":
				ctx = nil
			}
			file := filepath.Join(root, "lifecycle.json")
			raw, err := canonical.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "duplicate JSON":
				raw = bytes.Replace(raw, []byte(`"status":"complete"`), []byte(`"status":"failed","status":"complete"`), 1)
			case "noncanonical JSON":
				raw = append(raw, '\n')
			}
			writeTenantFixture(t, file, raw, 0600)
			switch tc.name {
			case "symlink record":
				if err := os.Rename(file, file+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+".saved", file); err != nil {
					t.Fatal(err)
				}
			case "symlink root":
				link := filepath.Join(privateTestDir(t), "link")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				root = link
			case "uncaptured", "missing original Plan":
				path := filepath.Join(root, "compile-inputs.json")
				if tc.name == "missing original Plan" {
					path = filepath.Join(root, ".runner", "plan.json")
				}
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
			case "unfinished disk commit":
				writeTenantFixture(t, filepath.Join(root, ".disk-commit-in-progress"), []byte("pending"), 0600)
			}
			plan, artifact, err := nativeSelectedRunPlan(ctx, root, selected, kind)
			if tc.code == "" {
				if err != nil || plan == nil || artifact.Name != "tools" {
					t.Fatalf("selected artifact=%+v error=%v", artifact, err)
				}
			} else {
				var coded *nvkrerr.Error
				if plan != nil || artifact.Name != "" || !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("rejected metadata yielded plan=%v artifact=%+v error=%v, want %s", plan != nil, artifact, err, tc.code)
				}
			}
		})
	}
}

func TestDigestFileSubstitutions(t *testing.T) {
	for _, name := range []string{"regular", "symlink", "directory", "FIFO", "canceled"} {
		t.Run(name, func(t *testing.T) {
			root := privateTestDir(t)
			file := filepath.Join(root, "blob")
			ctx := t.Context()
			switch name {
			case "directory":
				if err := os.Mkdir(file, 0o700); err != nil {
					t.Fatal(err)
				}
			case "FIFO":
				if err := unix.Mkfifo(file, 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "symlink" {
				held := filepath.Join(root, "held")
				if err := os.Rename(file, held); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(held, file); err != nil {
					t.Fatal(err)
				}
			}
			if name == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := DigestFile(ctx, file)
			if name == "regular" {
				want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("fixture")))
				if err != nil || got != want {
					t.Fatalf("digest %q, want %q, error %v", got, want, err)
				}
			} else if err == nil {
				t.Fatal("substituted file admitted")
			}
		})
	}
}

type assetEntry struct {
	name string
	kind byte
	body string
	mode int64
}

func writeAssetArchive(t *testing.T, entries []assetEntry) string {
	t.Helper()
	file := filepath.Join(privateTestDir(t), "assets.tgz")
	var raw bytes.Buffer
	zipped := gzip.NewWriter(&raw)
	writer := tar.NewWriter(zipped)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: entry.name, Typeflag: entry.kind, Mode: mode, Size: int64(len(entry.body))}
		if entry.kind == tar.TypeDir {
			header.Size = 0
		}
		if entry.kind == tar.TypeSymlink || entry.kind == tar.TypeLink {
			header.Linkname = "/foreign"
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestSafeExtractAssets(t *testing.T) {
	base := []assetEntry{{"internal/seed/default-seed.yaml", tar.TypeReg, "seed", 0}, {"ui/", tar.TypeDir, "", 0}, {"ui/index.html", tar.TypeReg, "index", 0}}
	for _, name := range []string{"valid", "traversal", "nested traversal", "backslash traversal", "absolute", "symlink", "hardlink", "device", "duplicate", "missing seed", "oversized", "too many", "privileged mode", "bad CRC", "destination collision", "destination symlink", "canceled"} {
		t.Run(name, func(t *testing.T) {
			entries := append([]assetEntry(nil), base...)
			out := privateTestDir(t)
			ctx := t.Context()
			switch name {
			case "traversal":
				entries = append(entries, assetEntry{"../outside", tar.TypeReg, "escape", 0})
			case "nested traversal":
				entries = append(entries, assetEntry{"ui/../../outside", tar.TypeReg, "escape", 0})
			case "backslash traversal":
				entries = append(entries, assetEntry{"..\\outside", tar.TypeReg, "escape", 0})
			case "absolute":
				entries = append(entries, assetEntry{"/outside", tar.TypeReg, "escape", 0})
			case "symlink":
				entries = append(entries, assetEntry{"linked", tar.TypeSymlink, "", 0})
			case "hardlink":
				entries = append(entries, assetEntry{"linked", tar.TypeLink, "", 0})
			case "device":
				entries = append(entries, assetEntry{"device", tar.TypeChar, "", 0})
			case "duplicate":
				entries = append(entries, base[0])
			case "missing seed":
				entries = entries[1:]
			case "oversized":
				entries = append(entries, assetEntry{"large", tar.TypeReg, strings.Repeat("x", 16<<20), 0})
			case "too many":
				for i := 0; i < 256; i++ {
					entries = append(entries, assetEntry{fmt.Sprintf("ui/file-%03d", i), tar.TypeReg, "x", 0})
				}
			case "privileged mode":
				entries[0].mode = 0o4755
			case "destination collision":
				if err := os.Mkdir(filepath.Join(out, "ui"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "destination symlink":
				if err := os.Symlink(privateTestDir(t), filepath.Join(out, "ui")); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			archive := writeAssetArchive(t, entries)
			if name == "bad CRC" {
				raw, err := os.ReadFile(archive)
				if err != nil {
					t.Fatal(err)
				}
				raw[len(raw)-8] ^= 0xff
				if err := os.WriteFile(archive, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := SafeExtractAssets(ctx, archive, out)
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range []assetEntry{base[0], base[2]} {
					got, err := os.ReadFile(filepath.Join(out, entry.name))
					if err != nil || string(got) != entry.body {
						t.Fatalf("bad member %q: %v", got, err)
					}
				}
			} else {
				if err == nil {
					t.Fatal("unsafe assets accepted")
				}
				if _, statErr := os.Stat(filepath.Join(out, "internal")); !os.IsNotExist(statErr) {
					t.Fatal("partial output after admission failure")
				}
			}
		})
	}
}

func domainFixture(t *testing.T) (Runner, string, string, string) {
	t.Helper()
	root := privateTestDir(t)
	work := privateTestDir(t)
	id := "a1b2c3d4-9999-4000-8000-000000000001"
	name := "nico-vm-" + id
	virsh := `#!/bin/sh
set -eu
test "$1" = -c
test "$2" = qemu:///session
case "$3" in
list) cat "$FIXTURE/inventory";;
dumpxml) cat "$FIXTURE/$4.xml";;
domuuid) cat "$FIXTURE/$4.uuid";;
domstate) printf 'running\n';;
destroy) test "$4" = "$OWNED_UUID"; printf '%s %s\n' "$3" "$4" >> "$FIXTURE/events";;
undefine) test "$4" = "$OWNED_UUID"; printf '%s %s\n' "$3" "$4" >> "$FIXTURE/events"; grep -v -F "$OWNED_NAME" "$FIXTURE/inventory" > "$FIXTURE/remaining" || :; mv "$FIXTURE/remaining" "$FIXTURE/inventory";;
*) exit 22;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "virsh"), []byte(virsh), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inventory"), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name+".uuid"), []byte(id), 0o600); err != nil {
		t.Fatal(err)
	}
	xml := fmt.Sprintf(`<domain><name>%s</name><uuid>%s</uuid><devices><disk><source file="%s"/></disk></devices></domain>`, name, id, filepath.Join(work, "var/instances", id, "disk.qcow2"))
	if err := os.WriteFile(filepath.Join(root, name+".xml"), []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Runner{Env: []string{"PATH=" + root + ":/usr/bin:/bin", "FIXTURE=" + root, "OWNED_UUID=" + id, "OWNED_NAME=" + name}}, root, work, name
}

func TestDomainExactOwnershipAndDeletion(t *testing.T) {
	for _, caseName := range []string{"owned", "foreign disk", "foreign name", "duplicate UUID", "foreign UUID", "duplicate disk", "malformed XML", "workspace symlink", "instance symlink", "disk symlink", "public workspace"} {
		t.Run(caseName, func(t *testing.T) {
			runner, root, work, name := domainFixture(t)
			file := filepath.Join(root, name+".xml")
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			switch caseName {
			case "foreign disk":
				raw = bytes.ReplaceAll(raw, []byte(work), []byte("/foreign"))
			case "foreign name":
				raw = bytes.Replace(raw, []byte("<name>"+name), []byte("<name>foreign"), 1)
			case "duplicate UUID":
				raw = bytes.Replace(raw, []byte("</uuid>"), []byte("</uuid><uuid>foreign</uuid>"), 1)
			case "foreign UUID":
				if err := os.WriteFile(filepath.Join(root, name+".uuid"), []byte("different"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "duplicate disk":
				begin := bytes.Index(raw, []byte("<disk>"))
				end := bytes.Index(raw, []byte("</disk>")) + len("</disk>")
				raw = bytes.Replace(raw, []byte("</devices>"), append(append([]byte(nil), raw[begin:end]...), []byte("</devices>")...), 1)
			case "malformed XML":
				raw = []byte("<domain")
			case "public workspace":
				if err := os.Chmod(work, 0o755); err != nil {
					t.Fatal(err)
				}
			case "workspace symlink":
				link := filepath.Join(privateTestDir(t), "link")
				if err := os.Symlink(work, link); err != nil {
					t.Fatal(err)
				}
				raw = bytes.ReplaceAll(raw, []byte(work), []byte(link))
				work = link
			case "instance symlink":
				if err := os.Symlink(privateTestDir(t), filepath.Join(work, "var")); err != nil {
					t.Fatal(err)
				}
			case "disk symlink":
				instance := filepath.Join(work, "var", "instances", strings.TrimPrefix(name, "nico-vm-"))
				if err := os.MkdirAll(instance, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(privateTestDir(t), "foreign"), filepath.Join(instance, "disk.qcow2")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(file, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			err = RemoveOwnedDomain(t.Context(), runner, name, work)
			if caseName == "owned" {
				if err != nil {
					t.Fatal(err)
				}
				events, err := os.ReadFile(filepath.Join(root, "events"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(events), strings.TrimPrefix(name, "nico-vm-")) != 2 {
					t.Fatalf("mutations not bound to UUID: %s", events)
				}
			} else {
				if err == nil {
					t.Fatal("foreign domain removed")
				}
				if _, err := os.Stat(filepath.Join(root, "events")); !os.IsNotExist(err) {
					t.Fatal("mutation preceded domain ownership")
				}
			}
		})
	}
}

func TestSweepAmbiguousCreatePreservesForeignDomain(t *testing.T) {
	runner, root, work, name := domainFixture(t)
	foreign := strings.Replace(name, "000000000001", "000000000002", 1)
	xml := fmt.Sprintf(`<domain><name>%s</name><uuid>foreign</uuid><devices><disk><source file="/foreign/disk.qcow2"/></disk></devices></domain>`, foreign)
	if err := os.WriteFile(filepath.Join(root, foreign+".xml"), []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inventory"), []byte(name+"\n"+foreign+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SweepOwnedDomains(t.Context(), runner, work); err != nil {
		t.Fatal(err)
	}
	names, err := DomainInventory(t.Context(), runner)
	if err != nil || len(names) != 1 || names[0] != foreign {
		t.Fatalf("foreign domain changed: %v %v", names, err)
	}
}
