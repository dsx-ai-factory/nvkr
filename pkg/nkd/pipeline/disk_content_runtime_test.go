package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func TestDiskContentRejectsIncompleteProfileBeforeOriginalArtifactWork(t *testing.T) {
	for _, kind := range []string{"disk", "acquisitions"} {
		t.Run(kind, func(t *testing.T) {
			container := &DiskInspectionContainer{selection: testDiskInspectionSelection(t)}
			artifact := &PackerArtifact{}
			artifact.mu.Lock()
			defer artifact.mu.Unlock()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var err error
			if kind == "disk" {
				_, err = container.InspectPackerContent(ctx, artifact, "linux/amd64")
			} else {
				_, err = container.InspectPackerAcquisitionsContent(ctx, artifact, "linux/amd64")
			}
			if code(err) != e.CodeEngineCapability {
				t.Fatalf("unadmitted content reached original artifact: %v", err)
			}
		})
	}
}

func TestDiskContentInvocationSeparatesNativeHostAndConsumer(t *testing.T) {
	for _, host := range []string{"amd64", "arm64"} {
		for _, consumer := range []string{"amd64", "arm64"} {
			t.Run(host+"-"+consumer, func(t *testing.T) {
				pin := "sha256:" + strings.Repeat("a", 64)
				selection := DiskInspectionSelection{Platform: "linux/" + host, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin, ScannerDigest: pin, PackageReaderDigest: pin,
					MaxFilesystemBytes: 1 << 20, ScratchStorage: "memory", compositionKind: "disk-image", consumerPlatform: "linux/" + consumer,
					Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 8192, EphemeralStorageMiB: 8192}}
				held, err := heldfile.FromBytesContext(t.Context(), []byte("subject"), heldfile.Options{Label: "content-argv", MaxBytes: 32})
				if err != nil {
					t.Fatal(err)
				}
				defer held.Close()
				args := diskInspectionCreateArgs(selection, pin, "attempt", "unique", held)
				if args[len(args)-4] != "linux/"+consumer || strings.Join(args[len(args)-3:], ",") != "1000,8192,256" {
					t.Fatal("consumer architecture was replaced by host architecture")
				}
				if strings.Contains(strings.Join(args, " "), "--network=host") {
					t.Fatal("content scanner changed isolation lane")
				}
			})
		}
	}
}

func TestDiskContentResourcesUseDeclaredExpansionBudget(t *testing.T) {
	for _, before := range []bool{false, true} {
		for _, tt := range []struct {
			name string
			edit func(*DiskInspectionSelection, *execution.ObservedHostFacts, *int64)
			want e.Code
		}{
			{name: "exact expansion grant"},
			{name: "memory one MiB short", edit: func(s *DiskInspectionSelection, _ *execution.ObservedHostFacts, _ *int64) { s.Resources.MemoryMiB-- }, want: e.CodeEngineCapability},
			{name: "host memory one MiB short", edit: func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts, _ *int64) { f.Resources.MemoryMiB-- }, want: e.CodeEngineCapability},
			{name: "host CPU one milliCPU short", edit: func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts, _ *int64) { f.Resources.CPUMillis-- }, want: e.CodeEngineCapability},
			{name: "workspace one MiB short", edit: func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts, _ *int64) {
				f.Resources.EphemeralStorageMiB--
			}, want: e.CodeEngineCapability},
			{name: "wrong native host", edit: func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts, _ *int64) {
				f.Platform = execution.HostPlatformLinuxARM64
			}, want: e.CodeEngineCapability},
			{name: "large subject does not fit grant", edit: func(_ *DiskInspectionSelection, _ *execution.ObservedHostFacts, n *int64) { *n = 8 << 30 }, want: e.CodeEngineCapability},
		} {
			t.Run(tt.name+map[bool]string{false: "/scan", true: "/snapshot"}[before], func(t *testing.T) {
				selection := testDiskInspectionSelection(t)
				selection.MaxFilesystemBytes = 1 << 20
				selection.ScratchStorage = "memory"
				// Two 1MiB subject copies, the appliance, all three four-copy
				// filesystem levels and 2GiB scanner metadata fit exactly.
				selection.Resources.MemoryMiB = 2 + 2048 + 12 + 2048
				facts := execution.ObservedHostFacts{Platform: execution.HostPlatformLinuxAMD64,
					Resources: execution.ResourceCapacity{CPUMillis: selection.Resources.CPUMillis, MemoryMiB: selection.Resources.MemoryMiB, EphemeralStorageMiB: selection.Resources.EphemeralStorageMiB}}
				if before {
					facts.Resources.MemoryMiB++
				}
				size := int64(1 << 20)
				if tt.edit != nil {
					tt.edit(&selection, &facts, &size)
				}
				if err := validateDiskInspectionResources(selection, facts, size, before); code(err) != tt.want {
					t.Fatalf("got %v, want %s", err, tt.want)
				}
			})
		}
	}
}

func TestDiskContentPrivateScratchUsesDiskInsteadOfExpandedRAM(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, before := range []bool{false, true} {
			for _, tc := range []struct {
				name                      string
				memoryDelta, storageDelta int64
				want                      e.Code
			}{
				{"large export fits bounded RAM", 0, 0, ""},
				{"RAM one MiB short", -1, 0, e.CodeEngineCapability},
				{"scratch one MiB short", 0, -1, e.CodeEngineCapability},
			} {
				t.Run(platform+"/"+tc.name+map[bool]string{true: "/snapshot", false: "/scan"}[before], func(t *testing.T) {
					selection := testDiskInspectionSelection(t)
					selection.Platform = platform
					selection.MaxFilesystemBytes = 16 << 30
					selection.ScratchStorage = "private-volume"
					selection.Resources.MemoryMiB = uint64(int64(2*1024+2048+6144+2*1024) + tc.memoryDelta)
					selection.Resources.EphemeralStorageMiB = uint64(int64(12*16384+2*1024+2048) + tc.storageDelta)
					facts := execution.ObservedHostFacts{Platform: execution.HostPlatform(platform), Resources: execution.ResourceCapacity{CPUMillis: selection.Resources.CPUMillis, MemoryMiB: selection.Resources.MemoryMiB, EphemeralStorageMiB: selection.Resources.EphemeralStorageMiB}}
					if before {
						facts.Resources.MemoryMiB += 1024
					}
					if err := validateDiskInspectionResources(selection, facts, 1<<30, before); code(err) != tc.want {
						t.Fatalf("got %v want%s", err, tc.want)
					}
				})
			}
		}
	}
}

func TestDiskContentScratchMountIsAttemptOwned(t *testing.T) {
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("subject"), heldfile.Options{Label: "scratch-argv", MaxBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	for _, tc := range []struct{ name, volume, want string }{
		{"historical diagnostic", "", "--tmpfs"},
		{"private composition", "nkd-inspect-012345678901234567890123-scratch", "--volume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := testDiskInspectionSelection(t)
			selection.scratchVolume = tc.volume
			args := diskInspectionCreateArgs(selection, selection.Image, "name", "attempt", subject)
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("missing%s: %v", tc.want, args)
			}
			if tc.volume != "" && (strings.Contains(joined, "--tmpfs") || !strings.Contains(joined, tc.volume+":/tmp:nocopy")) {
				t.Fatalf("scratch projection differs: %v", args)
			}
		})
	}
}
