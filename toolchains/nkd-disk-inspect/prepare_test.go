package diskinspect_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePreparationUsesForegroundInspectorLog(t *testing.T) {
	script, err := os.ReadFile("../../scripts/prepare-native-toolchains.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	if strings.Contains(text, "2> >(") {
		t.Fatal("inspector log writes must not escape the joined build/load pipeline")
	}
	if !strings.Contains(text, `2>"${output}/inspector-build.log" | timeout`) {
		t.Fatal("inspector build must retain its foreground failure log")
	}
}

func TestNativePreparationRejectsUnsupportedHostBeforeBuild(t *testing.T) {
	script, err := filepath.Abs("../../scripts/prepare-native-toolchains.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, arch, system, machine, message string }{
		{"unknown architecture", "riscv64", "Linux", "x86_64", "usage:"},
		{"macOS host", "arm64", "Darwin", "arm64", "native Linux"},
		{"ARM on AMD", "arm64", "Linux", "x86_64", "cross-architecture"},
		{"AMD on ARM", "amd64", "Linux", "aarch64", "cross-architecture"},
		{"unknown host machine", "amd64", "Linux", "riscv64", "unsupported native host architecture"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := t.TempDir()
			uname := "#!/bin/sh\ncase \"$1\" in -s) echo " + test.system + ";; -m) echo " + test.machine + ";; *) exit 1;; esac\n"
			if err := os.WriteFile(filepath.Join(bin, "uname"), []byte(uname), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "bash", script, test.arch)
			command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(out), test.message) {
				t.Fatalf("got %s, %v", out, err)
			}
		})
	}
}

func TestNativePreparationRejectsSourceAndOptionalInputsBeforeBuild(t *testing.T) {
	script, err := filepath.Abs("../../scripts/prepare-native-toolchains.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, gitStatus, variable, value, message string }{
		{"dirty source", " M file", "", "", "clean source checkout"},
		{"missing CA", "", "NKD_BUILD_CA_BUNDLE", "/nonexistent/nkd-build-ca", "CA bundle is not a regular file"},
		{"missing archives", "", "NKD_BUILD_ARCHIVES", "/nonexistent/nkd-archives", "archive context is not a directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := t.TempDir()
			for name, body := range map[string]string{
				"uname":  "#!/bin/sh\ncase \"$1\" in -s) echo Linux;; -m) echo x86_64;; esac\n",
				"git":    "#!/bin/sh\nprintf '%s' '" + test.gitStatus + "'\n",
				"docker": "#!/bin/sh\necho BUILD-MUST-NOT-START\nexit 99\n",
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.CommandContext(t.Context(), "bash", script, "amd64")
			command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "NKD_BUILD_CA_BUNDLE=", "NKD_BUILD_ARCHIVES=")
			if test.variable != "" {
				command.Env = append(command.Env, test.variable+"="+test.value)
			}
			out, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(out), test.message) || strings.Contains(string(out), "BUILD-MUST-NOT-START") {
				t.Fatalf("unsafe preparation admission: %s / %v", out, err)
			}
		})
	}
}
