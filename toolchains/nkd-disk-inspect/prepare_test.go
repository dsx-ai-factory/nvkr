package diskinspect_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
