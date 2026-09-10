package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/profile"
)

func writeCLIProfile(t *testing.T, p profile.BuilderProfile) string {
	t.Helper()
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProfileCLIInspectionPreservesCanonicalIdentity(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			p := profile.Embedded(arch)
			digest, err := p.Digest()
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := p.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			for _, flags := range [][]string{{"--arch", arch}, {"--profile", writeCLIProfile(t, p)}} {
				for _, command := range []string{"show", "digest"} {
					got, err := invokeLifecycleCLI(t, append([]string{"profile", command}, flags...)...)
					if err != nil {
						t.Fatal(err)
					}
					want := digest + "\n"
					if command == "show" {
						want = string(canonical) + "\n# profileDigest: " + digest + "\n"
					}
					if got != want {
						t.Fatalf("%s output = %q, want %q", command, got, want)
					}
				}
			}
		})
	}
}

func TestProfileCLIRepinWritesExactNewIdentity(t *testing.T) {
	for _, inplace := range []bool{false, true} {
		t.Run(map[bool]string{false: "new generation", true: "unsigned in place"}[inplace], func(t *testing.T) {
			p := profile.Embedded("amd64")
			source := writeCLIProfile(t, p)
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			runner, toolchain := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
			args := []string{"profile", "repin", "--profile", source, "--runner", runner, "--toolchain", toolchain}
			out := source
			if !inplace {
				out = filepath.Join(t.TempDir(), "repinned.json")
				args = append(args, "--out", out)
			}
			output, err := invokeLifecycleCLI(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			p.Repin(profile.FieldRunnerDigest, runner, true)
			p.Repin(profile.FieldBootstrapToolchainDigest, toolchain, true)
			want, err := p.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(out)
			if err != nil || string(got) != string(want) {
				t.Fatalf("repinned bytes = %s, %v", got, err)
			}
			digest, err := p.Digest()
			if err != nil || !strings.Contains(output, digest) {
				t.Fatalf("repin identity = %s, %v", output, err)
			}
			if !inplace {
				after, err := os.ReadFile(source)
				if err != nil || string(after) != string(before) {
					t.Fatalf("repin changed source: %v", err)
				}
			}
		})
	}
}

func TestProfileCLIRepinRejectsInvalidRequestsWithoutChangingSource(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		code  e.Code
	}{
		{"no changes", nil, e.CodeUsage},
		{"bad digest", []string{"--runner", "not-a-digest"}, e.CodeInvalidCatalog},
		{"signed requires destination", []string{"--sign"}, e.CodeUsage},
		{"signed cannot overwrite source", []string{"--sign", "--out", "SOURCE"}, e.CodeImmutableViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := writeCLIProfile(t, profile.Embedded("amd64"))
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			flags := append([]string(nil), tc.flags...)
			for i := range flags {
				if flags[i] == "SOURCE" {
					flags[i] = source
				}
			}
			_, err = invokeLifecycleCLI(t, append([]string{"profile", "repin", "--profile", source}, flags...)...)
			assertLifecycleCommandCode(t, err, tc.code)
			after, err := os.ReadFile(source)
			if err != nil || string(before) != string(after) {
				t.Fatalf("rejected repin changed source: %v", err)
			}
		})
	}
}

func TestProfileCLIPreflightReportsHostDivergence(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(map[bool]string{false: "enforced", true: "diagnostic"}[skip], func(t *testing.T) {
			p := profile.BuilderProfile{SchemaVersion: profile.SchemaVersion, Identity: "absent-host-fact", HostControlClass: profile.OperatorControlled, Fields: []profile.Field{{Name: "test.absent", Value: "required", HardFail: true}}}
			args := []string{"profile", "preflight", "--profile", writeCLIProfile(t, p)}
			if skip {
				args = append(args, "--skip-preflight")
			}
			output, err := invokeLifecycleCLI(t, args...)
			if skip {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertLifecycleCommandCode(t, err, e.CodePreflightFailed)
			}
			for _, want := range []string{"test.absent", "required", "<absent>", "true"} {
				if !strings.Contains(output, want) {
					t.Fatalf("missing %q: %s", want, output)
				}
			}
		})
	}
}
