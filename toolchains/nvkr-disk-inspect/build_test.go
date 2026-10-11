// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestDiagnosticDockerfileMatchesSourceLock(t *testing.T) {
	raw, err := os.ReadFile("toolchain.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		SchemaVersion    string             `json:"schemaVersion"`
		BaseImage        string             `json:"baseImage"`
		DownloadImage    string             `json:"downloadImage"`
		UbuntuSnapshot   string             `json:"ubuntuSnapshot"`
		Packages         map[string]string  `json:"packages"`
		Images           map[string]*string `json:"images"`
		PublicationGates map[string]string  `json:"publicationGates"`
		Scanner          struct {
			Version       string            `json:"version"`
			SchemaVersion string            `json:"schemaVersion"`
			Archives      map[string]string `json:"archives"`
		} `json:"scanner"`
		Helper struct {
			Source        string   `json:"source"`
			GoToolchain   string   `json:"goToolchain"`
			Architectures []string `json:"architectures"`
		} `json:"helper"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		t.Fatal(err)
	}
	goVersion, err := os.ReadFile("../../.go-version")
	if err != nil {
		t.Fatal(err)
	}
	if lock.Helper.Source != "cmd/nvkr-disk-inspect" || lock.Helper.GoToolchain != "go"+strings.TrimSpace(string(goVersion)) || !slices.Equal(lock.Helper.Architectures, []string{"amd64", "arm64"}) {
		t.Fatal("helper source, Go version or architectures differ from the source contract")
	}
	if info, err := os.Stat(filepath.Join("../..", lock.Helper.Source, "main.go")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("helper source absent: %v", err)
	}
	if lock.SchemaVersion != "nvkr.dev/disk-inspect-toolchain/v1" || len(lock.Packages) != 7 || len(lock.Images) != 2 || lock.Images["amd64"] != nil || lock.Images["arm64"] != nil || lock.PublicationGates["licenseApproval"] != "not-recorded" || lock.PublicationGates["vulnerabilityApproval"] != "not-recorded" {
		t.Fatal("diagnostic source lock has an unexpected contract or publication claim")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, ok := lock.Images[arch]; !ok {
			t.Fatalf("missing diagnostic image slot %s", arch)
		}
	}
	for _, image := range []string{lock.BaseImage, lock.DownloadImage} {
		if _, err := name.NewDigest(image); err != nil {
			t.Fatalf("source image is not digest-pinned: %v", err)
		}
	}
	data, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(data)
	if lock.Scanner.Version != "1.51.1" || lock.Scanner.SchemaVersion != "16.1.10" || len(lock.Scanner.Archives) != 2 {
		t.Fatal("scanner source does not match the reviewed full-inspector contract")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		pin := lock.Scanner.Archives[arch]
		if !canonical.IsDigest(pin) || !strings.Contains(dockerfile, arch+") scanner_sha="+strings.TrimPrefix(pin, "sha256:")) {
			t.Fatalf("missing exact native scanner archive %s", arch)
		}
	}
	for _, text := range []string{"FROM " + lock.BaseImage, "FROM " + lock.DownloadImage,
		"COPY --from=scanner /tmp/syft /usr/local/bin/syft",
		"https://github.com/anchore/syft/releases/download/v" + lock.Scanner.Version + "/syft_" + lock.Scanner.Version + "_linux_${TARGETARCH}.tar.gz",
		"sha256sum -c -;",
		"COPY --chmod=0755 bin/nvkr-disk-inspect-${TARGETARCH} /usr/local/bin/nkd-disk-inspect",
		"--max-time 300 --max-filesize 134217728",
		"sha256sum /usr/bin/virt-inspector /usr/bin/qemu-img /usr/bin/guestfish /usr/bin/dpkg-deb /usr/local/bin/syft",
		"USER 65534:65534",
		"https://snapshot.ubuntu.com/ubuntu/" + lock.UbuntuSnapshot,
		"amd64) qemu_package=qemu-system-x86 ;;",
		"arm64) qemu_package=qemu-system-arm ;;",
		"chmod 0644 /boot/vmlinuz-*;",
		"dpkg-query -W -f='${Package}\\t${Version}\\t${Architecture}\\n' \\\n      > /usr/share/nvkr-disk-inspect/packages.unsorted.tsv;",
		"LC_ALL=C sort /usr/share/nvkr-disk-inspect/packages.unsorted.tsv \\\n      > /usr/share/nvkr-disk-inspect/packages.tsv;",
		"rm /usr/share/nvkr-disk-inspect/packages.unsorted.tsv;",
		"/etc/apt/sources.list.d/ubuntu.sources /etc/ssl/certs/ca-certificates.crt"} {
		if !strings.Contains(dockerfile, text) {
			t.Fatalf("Dockerfile does not match lock/cleanup: %q", text)
		}
	}
	for name, version := range lock.Packages {
		text := name + "=" + version
		if strings.HasPrefix(name, "qemu-system-") {
			if !strings.Contains(dockerfile, "qemu_package="+name) {
				t.Fatalf("missing architecture package %s", name)
			}
			text = "${qemu_package}=" + version
		}
		if !strings.Contains(dockerfile, text) {
			t.Fatalf("missing locked package %s", text)
		}
	}
}

func TestNativeBuildAndMeasurementInstructions(t *testing.T) {
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(raw)
	for _, arch := range []string{"amd64", "arm64"} {
		for _, required := range []string{
			"GOOS=linux GOARCH=" + arch,
			"bin/nvkr-disk-inspect-" + arch,
			"podman build --platform linux/" + arch + " --build-arg TARGETARCH=" + arch,
			"localhost/nvkr-disk-inspect:local-" + arch,
		} {
			if !strings.Contains(readme, required) {
				t.Fatalf("native instructions omit %q", required)
			}
		}
	}
	for _, required := range []string{"--pull=never", "--network none --read-only --cap-drop ALL",
		"/usr/bin/virt-inspector /usr/bin/qemu-img /usr/bin/guestfish", "/usr/local/bin/syft /usr/bin/dpkg-deb",
		"/usr/local/bin/nkd-disk-inspect", "/usr/share/nvkr-disk-inspect/packages.tsv"} {
		if !strings.Contains(readme, required) {
			t.Fatalf("local measurement instructions omit %q", required)
		}
	}
}
