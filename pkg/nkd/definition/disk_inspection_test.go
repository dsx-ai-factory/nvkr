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
	for _, host := range []string{"linux/amd64", "linux/arm64"} {
		for _, name := range []string{"tag", "image CRLF", "missing inspector", "missing codec", "missing scanner", "missing exporter", "missing package reader", "pin CRLF", "CPU below runtime minimum", "zero CPU", "zero memory", "zero scratch capacity", "zero time", "unbounded time", "zero filesystem", "unbounded filesystem", "wrong host", "unknown scratch"} {
			t.Run(host+"/"+name, func(t *testing.T) {
				profile := inspectionProfileFixture()
				platform := host
				switch name {
				case "tag":
					profile.Image = "registry.example/nkd/inspect:latest"
				case "image CRLF":
					profile.Image += "\r\n"
				case "missing inspector":
					profile.InspectorDigest = ""
				case "missing codec":
					profile.CodecDigest = ""
				case "missing scanner":
					profile.ScannerDigest = ""
				case "missing exporter":
					profile.GuestfishDigest = ""
				case "missing package reader":
					profile.PackageReaderDigest = ""
				case "pin CRLF":
					profile.ScannerDigest += "\r\n"
				case "zero memory":
					profile.Resources.MemoryMiB = 0
				case "CPU below runtime minimum":
					profile.Resources.CPUMillis = 9
				case "zero CPU":
					profile.Resources.CPUMillis = 0
				case "zero scratch capacity":
					profile.Resources.EphemeralStorageMiB = 0
				case "zero time":
					profile.TimeoutSeconds = 0
				case "unbounded time":
					profile.TimeoutSeconds = 601
				case "unbounded filesystem":
					profile.MaxFilesystemBytes = 1 << 40
				case "zero filesystem":
					profile.MaxFilesystemBytes = 0
				case "wrong host":
					platform = "darwin/arm64"
				case "unknown scratch":
					profile.ScratchStorage = "host-bind"
				}
				var coded *e.Error
				want := e.CodeEngineCapability
				if name == "tag" || name == "image CRLF" || strings.HasPrefix(name, "missing ") || name == "pin CRLF" {
					want = e.CodeUnpinnedInput
				}
				if err := profile.Validate(platform); !errors.As(err, &coded) || coded.Code != want {
					t.Fatalf("invalid profile error = %v, want %s", err, want)
				}
			})
		}
	}
}

func TestEnvironmentDiskProfileFailureOrderIsDeterministic(t *testing.T) {
	amd64, arm64 := inspectionProfileFixture(), inspectionProfileFixture()
	amd64.ScannerDigest = ""
	arm64.Resources.MemoryMiB = 0
	environment := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind,
		Metadata: Metadata{Name: "fixture"}, UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64",
		DiskInspectionProfiles: map[string]DiskInspectionProfile{"linux/arm64": arm64, "linux/amd64": amd64}}
	for range 256 {
		err := environment.Validate()
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeUnpinnedInput {
			t.Fatalf("profile validation did not consistently select sorted AMD64 profile: %v", err)
		}
	}
}
