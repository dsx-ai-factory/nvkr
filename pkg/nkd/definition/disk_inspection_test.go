package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func inspectionProfileFixture() DiskInspectionProfile {
	pin := "sha256:" + strings.Repeat("a", 64)
	return DiskInspectionProfile{Image: "registry.example/nkd/inspect@" + pin, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin, ScannerDigest: pin, PackageReaderDigest: pin,
		Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 8192, EphemeralStorageMiB: 16384}, TimeoutSeconds: 600, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume"}
}

func TestDiskInspectionProfilesAreExactNativeAndCloned(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			profile := inspectionProfileFixture()
			if err := profile.Validate(platform); err != nil {
				t.Fatal(err)
			}
			environment := Environment{DiskInspectionProfiles: map[string]DiskInspectionProfile{platform: profile}}
			clone := environment.Clone()
			changed := clone.DiskInspectionProfiles[platform]
			changed.ScannerDigest = "sha256:" + strings.Repeat("b", 64)
			clone.DiskInspectionProfiles[platform] = changed
			if environment.DiskInspectionProfiles[platform] != profile {
				t.Fatal("clone mutated frozen profile")
			}
		})
	}
	for _, name := range []string{"tag", "missing scanner", "missing exporter", "missing package reader", "CPU below runtime minimum", "zero memory", "unbounded time", "unbounded filesystem", "wrong host", "unknown scratch"} {
		t.Run(name, func(t *testing.T) {
			profile := inspectionProfileFixture()
			platform := "linux/arm64"
			switch name {
			case "tag":
				profile.Image = "registry.example/nkd/inspect:latest"
			case "missing scanner":
				profile.ScannerDigest = ""
			case "missing exporter":
				profile.GuestfishDigest = ""
			case "missing package reader":
				profile.PackageReaderDigest = ""
			case "zero memory":
				profile.Resources.MemoryMiB = 0
			case "CPU below runtime minimum":
				profile.Resources.CPUMillis = 9
			case "unbounded time":
				profile.TimeoutSeconds = 601
			case "unbounded filesystem":
				profile.MaxFilesystemBytes = 1 << 40
			case "wrong host":
				platform = "darwin/arm64"
			case "unknown scratch":
				profile.ScratchStorage = "host-bind"
			}
			var coded *e.Error
			if err := profile.Validate(platform); !errors.As(err, &coded) {
				t.Fatalf("invalid profile did not fail coded: %v", err)
			}
		})
	}
}
