// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

func shellLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func zizmorShell(t *testing.T, root, body string, env []string, args ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs("../../scripts/zizmor-report.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", append([]string{"-c", "source \"$1\"; shift; " + body, "zizmor-test", script}, args...)...)
	command.Dir = root
	command.Env = scratchGitEnv(os.Environ(), append([]string{"GITHUB_STEP_SUMMARY=", "TMPDIR=" + root}, env...)...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err = ownedcmd.Run(ctx, command)
	return output.String(), err
}

func putFixture(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestZizmorCollection(t *testing.T) {
	for _, mode := range []string{"complete", "missing", "symlink", "empty"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			runIn(t, root, "git", "init", "-q")
			names := []string{".github/nvkr-run/actions/bootstrap/action.yml", ".github/workflows/authored.yaml", ".github/workflows/nvkr-run.yml"}
			if mode != "empty" {
				for _, name := range names {
					putFixture(t, filepath.Join(root, name), "fixture", 0o600)
				}
				runIn(t, root, "git", "add", ".github")
				putFixture(t, filepath.Join(root, ".github/workflows/new file.yaml"), "untracked", 0o600)
				putFixture(t, filepath.Join(root, ".github/workflows/nested/ignored.yml"), "not a workflow", 0o600)
				if mode == "missing" || mode == "symlink" {
					name := filepath.Join(root, names[0])
					if err := os.Rename(name, filepath.Join(root, "saved")); err != nil {
						t.Fatal(err)
					}
					if mode == "symlink" {
						if err := os.Symlink(filepath.Join(root, "saved"), name); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			work := t.TempDir()
			inputs := filepath.Join(work, "inputs.json")
			output, err := zizmorShell(t, root, `timer=$(zizmor_timer); zizmor_collect "$1" "$2" "$3"`, nil, root, work, inputs)
			if mode != "complete" {
				want := "missing or non-regular"
				if mode == "empty" {
					want = "no workflow/action inputs"
				}
				if err == nil || !strings.Contains(output, want) {
					t.Fatalf("%v: %s", err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %s", err, output)
			}
			var got []string
			if err := json.Unmarshal(readFile(t, inputs), &got); err != nil {
				t.Fatal(err)
			}
			want := append(names, ".github/workflows/new file.yaml")
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("inputs %q want %q", got, want)
			}
		})
	}
}

func zizmorArchive(t *testing.T, kind byte, name string) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	if name != "" {
		header := &tar.Header{Name: name, Mode: 0o700, Typeflag: kind, Linkname: "elsewhere"}
		if kind == tar.TypeReg {
			header.Size = 4
			header.Linkname = ""
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte("tool")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestZizmorInstall(t *testing.T) {
	for _, tc := range []struct {
		name, member string
		kind         byte
		mismatch     bool
	}{
		{"verified regular", "zizmor", tar.TypeReg, false}, {"checksum mismatch", "zizmor", tar.TypeReg, true},
		{"missing executable", "", tar.TypeReg, false}, {"symlink", "zizmor", tar.TypeSymlink, false}, {"hardlink", "zizmor", tar.TypeLink, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			work := t.TempDir()
			bin := filepath.Join(root, "bin")
			archive := zizmorArchive(t, tc.kind, tc.member)
			archivePath := filepath.Join(root, "fixture.tar.gz")
			if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(archive))
			if tc.mismatch {
				digest = strings.Repeat("0", 64)
			}
			putFixture(t, filepath.Join(bin, "curl"), "#!/bin/sh\nwhile [ \"$1\" != --output ]; do shift; done\ncp \"$ZIZMOR_ARCHIVE\" \"$2\"\n", 0o700)
			tarPath, err := exec.LookPath("tar")
			if err != nil {
				t.Fatal(err)
			}
			putFixture(t, filepath.Join(bin, "tar"), "#!/bin/sh\nprintf called >> \"$ZIZMOR_TAR_CALLS\"\nexec "+shellLiteral(tarPath)+" \"$@\"\n", 0o700)
			env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "ZIZMOR_ARCHIVE=" + archivePath, "ZIZMOR_TAR_CALLS=" + filepath.Join(root, "tar-calls")}
			body := `timer=$(zizmor_timer); digest=$2; zizmor_pin() { printf 'fixture %s\n' "$digest"; }; zizmor_install "$1"`
			output, err := zizmorShell(t, root, body, env, work, digest)
			if tc.name == "verified regular" {
				if err != nil {
					t.Fatalf("%v: %s", err, output)
				}
				if string(readFile(t, filepath.Join(work, "zizmor"))) != "tool" {
					t.Fatal("wrong extracted bytes")
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %s", tc.name)
			}
			if _, err := os.Stat(filepath.Join(work, "zizmor")); !os.IsNotExist(err) {
				t.Fatal("extracted rejected archive")
			}
			if tc.mismatch {
				if !strings.Contains(output, "checksum mismatch") {
					t.Fatal(output)
				}
				if _, err := os.Stat(filepath.Join(root, "tar-calls")); !os.IsNotExist(err) {
					t.Fatal("tar ran before checksum admission")
				}
			}
		})
	}
}

func TestZizmorUnsupportedPlatform(t *testing.T) {
	for _, platform := range [][2]string{{"Windows", "AMD64"}, {"Linux", "aarch64"}} {
		t.Run(strings.Join(platform[:], "/"), func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			putFixture(t, filepath.Join(bin, "uname"), "#!/bin/sh\ncase \"$1\" in -s) printf '%s\\n' \"$ZIZMOR_SYSTEM\";; *) printf '%s\\n' \"$ZIZMOR_MACHINE\";; esac\n", 0o700)
			putFixture(t, filepath.Join(bin, "curl"), "#!/bin/sh\nprintf called > \"$ZIZMOR_DOWNLOAD\"\nexit 1\n", 0o700)
			output, err := zizmorShell(t, root, `timer=$(zizmor_timer); zizmor_install "$1"`, []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "ZIZMOR_SYSTEM=" + platform[0], "ZIZMOR_MACHINE=" + platform[1], "ZIZMOR_DOWNLOAD=" + filepath.Join(root, "download")}, root)
			if err == nil || !strings.Contains(output, "unsupported zizmor platform") {
				t.Fatalf("%v: %s", err, output)
			}
			if _, err := os.Stat(filepath.Join(root, "download")); !os.IsNotExist(err) {
				t.Fatal("unsupported platform downloaded a tool")
			}
		})
	}
}

const zizmorFinding = `{"ident":"unpinned-uses","desc":"unpinned action","determinations":{"severity":"High"},"ignored":false,"locations":[{"symbolic":{"key":{"Local":{"verbatim_path":".github/workflows/test.yml"}},"annotation":"pin this action"},"concrete":{"location":{"start_point":{"row":6,"column":2}}}}]}`

func TestZizmorReportVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, raw, stderr, verdict string
		toolStatus                 int
	}{
		{"clean", "[]", "", "NO FINDINGS", 0}, {"findings", "[" + zizmorFinding + "]", "", "FINDINGS", 0},
		{"bad JSON", "{", "", "INCOMPLETE", 0}, {"bad schema", "{}", "", "INCOMPLETE", 0}, {"missing fields", "[{}]", "", "INCOMPLETE", 0},
		{"invalid UTF-8 findings", "[" + strings.Replace(zizmorFinding, "pin this action", "bad\xfftext", 1) + "]", "", "INCOMPLETE", 0},
		{"invalid UTF-8 diagnostics", "[]", " INFO audit: bad\xfftext", "INCOMPLETE", 0},
		{"tool failure", "[]", "failed collection", "INCOMPLETE", 1},
		{"INFO filename", "[]", " INFO audit: completed .github/workflows/WARN.yaml\n INFO audit: completed ERROR.yml", "NO FINDINGS", 0},
		{"audit error", "[]", " INFO audit: starting\nERROR audit: failed", "INCOMPLETE", 0}, {"audit warning", "[]", " WARN audit: could not parse expression", "INCOMPLETE", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			runIn(t, root, "git", "init", "-q")
			putFixture(t, filepath.Join(root, ".github/workflows/test.yml"), "fixture", 0o600)
			putFixture(t, filepath.Join(root, "raw"), tc.raw, 0o600)
			putFixture(t, filepath.Join(root, "stderr"), tc.stderr, 0o600)
			tool := filepath.Join(root, "zizmor")
			putFixture(t, tool, fmt.Sprintf("#!/bin/sh\n[ \"$RUST_LOG\" = info ] || exit 98\nprintf '%%s\\000' \"$@\" > \"$ZIZMOR_CALLS\"\ncat \"$ZIZMOR_RAW\"\ncat \"$ZIZMOR_STDERR\" >&2\nexit %d\n", tc.toolStatus), 0o700)
			output, err := zizmorShell(t, root, `zizmor_install() { printf '%s\n' "$ZIZMOR_TOOL"; }; zizmor_report "$1" "$2"`, []string{"ZIZMOR_TOOL=" + tool, "ZIZMOR_RAW=" + filepath.Join(root, "raw"), "ZIZMOR_STDERR=" + filepath.Join(root, "stderr"), "ZIZMOR_CALLS=" + filepath.Join(root, "calls")}, root, filepath.Join(root, "reports"))
			if (err != nil) != (tc.verdict == "INCOMPLETE") {
				t.Fatalf("%v: %s", err, output)
			}
			attempts, err := filepath.Glob(filepath.Join(root, "reports/attempt-*"))
			if err != nil || len(attempts) != 1 {
				t.Fatalf("attempts %v: %v", attempts, err)
			}
			if summary := string(readFile(t, filepath.Join(attempts[0], "summary.txt"))); !strings.Contains(summary, ": "+tc.verdict+"\n") {
				t.Fatal(summary)
			}
			if got := string(readFile(t, filepath.Join(attempts[0], "findings.json"))); got != tc.raw {
				t.Fatalf("lost tool bytes: %q", got)
			}
			calls := strings.Split(strings.TrimSuffix(string(readFile(t, filepath.Join(root, "calls"))), "\x00"), "\x00")
			want := []string{"--offline", "--strict-collection", "--no-progress", "--color", "never", "--format", "json-v1", "--no-exit-codes", ".github/workflows/test.yml"}
			if !slices.Equal(calls, want) {
				t.Fatalf("tool args %q", calls)
			}
			if tc.name == "findings" && !strings.Contains(string(readFile(t, filepath.Join(attempts[0], "findings.txt"))), "  .github/workflows/test.yml:7: pin this action") {
				t.Fatal("missing rendered location")
			}
		})
	}
}

func TestZizmorInstallFailureReport(t *testing.T) {
	for _, failure := range []string{"download failed", "checksum mismatch", "download timed out"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			runIn(t, root, "git", "init", "-q")
			putFixture(t, filepath.Join(root, ".github/workflows/test.yml"), "fixture", 0o600)
			output, err := zizmorShell(t, root, `zizmor_install() { zizmor_error "$ZIZMOR_FAILURE"; }; zizmor_report "$1" "$2"`, []string{"ZIZMOR_FAILURE=" + failure}, root, filepath.Join(root, "reports"))
			if err == nil || !strings.Contains(output, "INCOMPLETE") {
				t.Fatalf("%v: %s", err, output)
			}
			attempts, err := filepath.Glob(filepath.Join(root, "reports/attempt-*"))
			if err != nil || len(attempts) != 1 {
				t.Fatalf("attempts %v: %v", attempts, err)
			}
			if summary := string(readFile(t, filepath.Join(attempts[0], "summary.txt"))); !strings.Contains(summary, failure) {
				t.Fatal(summary)
			}
		})
	}
}
