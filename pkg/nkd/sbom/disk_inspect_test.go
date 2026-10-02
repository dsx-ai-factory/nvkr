package sbom

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

const diskPackageXML = `<operatingsystems><operatingsystem><name>linux</name><package_format>deb</package_format><applications><application><name>pkg</name><version>2.0</version><release>1ubuntu1</release><arch>arm64</arch></application></applications></operatingsystem></operatingsystems>`

func TestDiskInspectorInvocation(t *testing.T) {
	if got := diskInspectorArguments("/private/subject.qcow2"); !slices.Equal(got,
		[]string{"--format=qcow2", "--keys-from-stdin", "--no-icon", "-a", "/private/subject.qcow2"}) {
		t.Fatalf("unexpected inspector arguments: %v", got)
	}
	want := []string{"PATH=/usr/bin:/bin", "HOME=/private", "TMPDIR=/private", "XDG_CONFIG_HOME=/private",
		"LC_ALL=C", "TZ=UTC", "LIBGUESTFS_BACKEND=direct", "LIBGUESTFS_BACKEND_SETTINGS=force_tcg"}
	if got := diskInspectorEnvironment("/private"); !slices.Equal(got, want) {
		t.Fatalf("unexpected inspector environment: %v", got)
	}
}

func TestDiskInstalledPackageProjection(t *testing.T) {
	for _, tc := range []struct {
		name, xml string
		code      e.Code
	}{
		{"valid", diskPackageXML, ""},
		{"invalid XML", "not XML", e.CodeEnvelopeDecode},
		{"wrong root", "<other/>", e.CodeEnvelopeDecode},
		{"namespaced root", strings.Replace(diskPackageXML, "<operatingsystems>", `<operatingsystems xmlns="other">`, 1), e.CodeEnvelopeDecode},
		{"second document", diskPackageXML + diskPackageXML, e.CodeEnvelopeDecode},
		{"no system", "<operatingsystems/>", e.CodeEnvelopeDecode},
		{"two systems", strings.Replace(diskPackageXML, "</operatingsystems>", "<operatingsystem/></operatingsystems>", 1), e.CodeEnvelopeDecode},
		{"unsupported OS", strings.Replace(diskPackageXML, "linux", "windows", 1), e.CodeEngineCapability},
		{"unsupported package format", strings.Replace(diskPackageXML, "deb", "rpm", 1), e.CodeEngineCapability},
		{"duplicate package field", strings.Replace(diskPackageXML, "<name>pkg</name>", "<name>pkg</name><name>other</name>", 1), e.CodeEnvelopeDecode},
		{"duplicate OS field", strings.Replace(diskPackageXML, "<name>linux</name>", "<name>linux</name><name>linux</name>", 1), e.CodeEngineCapability},
		{"control in package", strings.Replace(diskPackageXML, "<name>pkg</name>", "<name>pkg&#10;</name>", 1), e.CodeEnvelopeDecode},
		{"oversized package field", strings.Replace(diskPackageXML, "<name>pkg</name>", "<name>"+strings.Repeat("a", 4097)+"</name>", 1), e.CodeEnvelopeDecode},
		{"unknown version stays unknown", strings.Replace(diskPackageXML, "<version>2.0</version>", "", 1), ""},
		{"empty optional field", strings.Replace(diskPackageXML, "<release>1ubuntu1</release>", "<release/>", 1), ""},
		{"oversized report", strings.Repeat(" ", maxDiskPackageReportBytes+1), e.CodeEnvelopeDecode},
		{"package allocation budget", "<operatingsystems>" + strings.Repeat("<application/>", maxDiskInstalledPackages+1) + "</operatingsystems>", e.CodeEnvelopeDecode},
		{"empty inventory", strings.Replace(diskPackageXML, "<applications>", "<unused>", 1), e.CodeEnvelopeDecode},
		{"no applications", "<operatingsystems><operatingsystem><name>linux</name><package_format>deb</package_format></operatingsystem></operatingsystems>", e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packages, err := installedPackagesFromXML([]byte(tc.xml))
			if tc.code != "" {
				assertDependencyCode(t, err, tc.code)
				if packages != nil {
					t.Fatal("failed projection returned packages")
				}
				return
			}
			if err != nil || len(packages) != 1 || packages[0].Name != "pkg" || packages[0].Format != "deb" {
				t.Fatalf("projection: %+v, %v", packages, err)
			}
			if tc.name == "unknown version stays unknown" && packages[0].Version != "" {
				t.Fatal("projection invented a version")
			}
		})
	}
}

func TestDiskInstalledPackageOrderingAndDuplicates(t *testing.T) {
	app := `<application><name>pkg</name><version>1</version><arch>arm64</arch></application>`
	for _, tc := range []struct {
		name, apps string
		count      int
		code       e.Code
	}{
		{"identical observations", app + app, 1, ""},
		{"separate architectures", app + strings.Replace(app, "arm64", "all", 1), 2, ""},
		{"conflicting package", app + strings.Replace(app, "<version>1</version>", "<version>2</version>", 1), 0, e.CodeVerifyFailed},
		{"sorted metadata", strings.Replace(app, "pkg", "z", 1) + app, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `<operatingsystems><operatingsystem><name>linux</name><package_format>deb</package_format><applications>` + tc.apps + `</applications></operatingsystem></operatingsystems>`
			packages, err := installedPackagesFromXML([]byte(raw))
			if tc.code != "" {
				assertDependencyCode(t, err, tc.code)
				return
			}
			if err != nil || len(packages) != tc.count {
				t.Fatalf("projection: %+v, %v", packages, err)
			}
			if tc.name == "sorted metadata" && packages[0].Name != "pkg" {
				t.Fatal("packages not sorted")
			}
		})
	}
}

// This acceptance test is opt-in because it boots a real libguestfs appliance
// and snapshots a complete disk. Contract tests above are not its substitute.
func TestDiskInspectionRealQCOW2(t *testing.T) {
	path := os.Getenv("NKD_DISK_INSPECT_SUBJECT")
	if path == "" {
		t.Skip("real QCOW2 and reviewed Linux inspector runtime not supplied")
	}
	ctx := t.Context()
	codec, err := assemble.OpenQemuImgCodec(ctx, os.Getenv("NKD_DISK_QEMU_PATH"), os.Getenv("NKD_DISK_QEMU_DIGEST"))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	inspector, err := OpenDiskInspector(ctx, os.Getenv("NKD_DISK_INSPECTOR_PATH"), os.Getenv("NKD_DISK_INSPECTOR_DIGEST"), codec)
	if err != nil {
		t.Fatal(err)
	}
	defer inspector.Close()
	subject, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "real diagnostic QCOW2", MaxBytes: MaxDiagnosticDiskBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	inventory, err := inspector.InspectHeldInstalledPackages(ctx, subject, "qcow2", "none")
	if err != nil {
		t.Fatal(err)
	}
	digest, size, _, _ := inventory.Binding()
	if digest != subject.Digest() || size != subject.Size() || len(inventory.Packages()) == 0 {
		t.Fatal("real inspection did not bind installed packages to exact subject")
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("diagnostic only: QCOW2 %s bytes=%d installed-packages=%d", digest, size, len(inventory.Packages()))
}

func TestDiskInspectionRealEncryptedWholeDevice(t *testing.T) {
	if os.Getenv("NKD_DISK_INSPECT_SUBJECT") == "" {
		t.Skip("reviewed Linux inspector runtime not supplied")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	codec, err := assemble.OpenQemuImgCodec(ctx, os.Getenv("NKD_DISK_QEMU_PATH"), os.Getenv("NKD_DISK_QEMU_DIGEST"))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	inspector, err := OpenDiskInspector(ctx, os.Getenv("NKD_DISK_INSPECTOR_PATH"), os.Getenv("NKD_DISK_INSPECTOR_DIGEST"), codec)
	if err != nil {
		t.Fatal(err)
	}
	defer inspector.Close()
	raw := filepath.Join(t.TempDir(), "encrypted.raw")
	file, err := os.Create(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(32 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// Public test-only key, never a production credential. Upstream cryptsetup
	// creates the fixture; neither NKD nor this test implements LUKS formatting.
	crypt := exec.CommandContext(ctx, "/usr/sbin/cryptsetup", "luksFormat", "--batch-mode", "--type", "luks2", "--key-file", "-", raw)
	crypt.Stdin = bytes.NewBufferString("nkd-public-test-key-not-a-secret")
	if output, err := crypt.CombinedOutput(); err != nil {
		t.Fatalf("creating LUKS fixture: %v: %s", err, output)
	}
	image := filepath.Join(t.TempDir(), "encrypted.qcow2")
	convert := exec.CommandContext(ctx, os.Getenv("NKD_DISK_QEMU_PATH"), "convert", "-f", "raw", "-O", "qcow2", raw, image)
	if output, err := convert.CombinedOutput(); err != nil {
		t.Fatalf("converting LUKS fixture: %v: %s", err, output)
	}
	subject, err := heldfile.OpenContext(ctx, image, heldfile.Options{Label: "encrypted guest fixture", MaxBytes: MaxDiagnosticDiskBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	inventory, err := inspector.InspectHeldInstalledPackages(ctx, subject, "qcow2", "none")
	assertDependencyCode(t, err, e.CodeEnvelopeDecode)
	if inventory != nil {
		t.Fatalf("encrypted whole device issued an inventory: %v", err)
	}
	t.Logf("encrypted whole-device rejection, not a partition-key EOF proof: %v", err)
}

func TestDiskInspectionRealExternalReferences(t *testing.T) {
	if os.Getenv("NKD_DISK_INSPECT_SUBJECT") == "" {
		t.Skip("reviewed Linux QEMU runtime not supplied")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	codec, err := assemble.OpenQemuImgCodec(ctx, os.Getenv("NKD_DISK_QEMU_PATH"), os.Getenv("NKD_DISK_QEMU_DIGEST"))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	for _, kind := range []string{"data", "backing"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			image, external := filepath.Join(workspace, "subject.qcow2"), filepath.Join(workspace, "missing.raw")
			args := []string{"create", "-f", "qcow2", "-o", "data_file=" + external, image, "1M"}
			if kind == "backing" {
				args = []string{"create", "-f", "qcow2", "-u", "-b", external, "-F", "raw", image, "1M"}
			}
			if output, err := exec.CommandContext(ctx, os.Getenv("NKD_DISK_QEMU_PATH"), args...).CombinedOutput(); err != nil {
				t.Fatalf("creating external-reference fixture: %v: %s", err, output)
			}
			if kind == "data" {
				if err := os.Remove(external); err != nil {
					t.Fatal(err)
				}
			}
			subject, err := heldfile.OpenContext(ctx, image, heldfile.Options{Label: "external-reference fixture", MaxBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			err = codec.ValidateHeldEnvelope(ctx, "qcow2", subject)
			assertDependencyCode(t, err, e.CodeEnvelopeDecode)
			if !strings.Contains(err.Error(), "not self-contained") {
				t.Fatalf("probe did not reject metadata without opening missing reference: %v", err)
			}
		})
	}
}

func TestDiskInspectionRealCancellation(t *testing.T) {
	if os.Getenv("NKD_DISK_INSPECT_SUBJECT") == "" {
		t.Skip("reviewed Linux inspector runtime not supplied")
	}
	ctx, deadline := context.WithTimeout(t.Context(), time.Minute)
	defer deadline()
	codec, err := assemble.OpenQemuImgCodec(ctx, os.Getenv("NKD_DISK_QEMU_PATH"), os.Getenv("NKD_DISK_QEMU_DIGEST"))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	inspector, err := OpenDiskInspector(ctx, os.Getenv("NKD_DISK_INSPECTOR_PATH"), os.Getenv("NKD_DISK_INSPECTOR_DIGEST"), codec)
	if err != nil {
		t.Fatal(err)
	}
	defer inspector.Close()
	image := filepath.Join(t.TempDir(), "blank.qcow2")
	if output, err := exec.CommandContext(ctx, os.Getenv("NKD_DISK_QEMU_PATH"), "create", "-f", "qcow2", image, "64M").CombinedOutput(); err != nil {
		t.Fatalf("creating cancellation fixture: %v: %s", err, output)
	}
	subject, err := heldfile.OpenContext(ctx, image, heldfile.Options{Label: "cancellation fixture", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		inventory, err := inspector.InspectHeldInstalledPackages(child, subject, "qcow2", "none")
		if inventory != nil {
			err = fmt.Errorf("canceled inspection issued an inventory")
		}
		done <- err
	}()
	// This opt-in test runs in its own PID-namespace container. Observe the real
	// QEMU appliance before canceling, then prove the owned process group joined.
	var processes []byte
	var group string
	for {
		processes, err = exec.CommandContext(ctx, "/bin/ps", "-eo", "pid,pgid,stat,args").Output()
		// Ignore short QMP capability probes; cancel the actual -kernel VM.
		for _, line := range strings.Split(string(processes), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 4 && strings.Contains(fields[3], "qemu-system") && slices.Contains(fields[4:], "-kernel") {
				group = fields[1]
			}
		}
		if err != nil || group != "" {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case err = <-done:
			t.Fatalf("inspection ended before an appliance could be canceled: %v", err)
		case <-time.After(20 * time.Millisecond):
			continue
		}
		break
	}
	cancel()
	result := <-done // The child uses the one-minute parent deadline and is joined.
	if err != nil {
		t.Fatalf("observing real appliance before cancellation: %v", err)
	}
	if pgid, err := strconv.Atoi(group); err != nil || pgid <= 0 {
		t.Fatalf("real appliance reported invalid process-group ID %q", group)
	}
	t.Logf("appliance process groups before cancel:\n%s", processes)
	assertDependencyCode(t, result, e.CodeTimeout)
	reapCtx, stopReaping := context.WithTimeout(ctx, 2*time.Second)
	defer stopReaping()
	var after []byte
	for {
		after, err = exec.CommandContext(reapCtx, "/bin/ps", "-eo", "pid,pgid,stat,comm").Output()
		if err != nil {
			t.Fatalf("checking appliance process-group cleanup: %v\n%s", err, after)
		}
		groupPresent := false
		for _, line := range strings.Split(string(after), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == group {
				groupPresent = true
			}
		}
		if !groupPresent {
			break
		}
		select {
		case <-reapCtx.Done():
			t.Fatalf("appliance descendants survived cancellation: %v\n%s", reapCtx.Err(), after)
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Logf("process groups after joined cancellation:\n%s", after)
}

func TestDiskInspectionCancellationBeforeSnapshotVerification(t *testing.T) {
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("test subject"), heldfile.Options{Label: "cancellation fixture", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inventory, err := inspectHeldDiskPackages(ctx, subject, "sha256:"+strings.Repeat("a", 64),
		func(_ context.Context, _, _ string, output, _ io.Writer) error {
			cancel()
			_, err := io.WriteString(output, diskPackageXML)
			return err
		})
	assertDependencyCode(t, err, e.CodeTimeout)
	if inventory != nil {
		t.Fatal("canceled snapshot verification issued an inventory")
	}
}

func TestDiskInspectionSnapshotProtocol(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
		run  diskInspectCommand
	}{
		{"valid", "", func(_ context.Context, path, _ string, out, _ io.Writer) error {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o400 {
				return fmt.Errorf("snapshot is not read-only: %v", err)
			}
			_, err = io.WriteString(out, diskPackageXML)
			return err
		}},
		{"tool failed", e.CodeEnvelopeDecode, func(context.Context, string, string, io.Writer, io.Writer) error {
			return fmt.Errorf("failed fixture inspector")
		}},
		{"warning", e.CodeEnvelopeDecode, func(_ context.Context, _, _ string, out, diagnostic io.Writer) error {
			_, _ = io.WriteString(out, diskPackageXML)
			_, err := io.WriteString(diagnostic, "incomplete mount")
			return err
		}},
		{"snapshot substitution", e.CodeVerifyFailed, func(_ context.Context, path, _ string, out, _ io.Writer) error {
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
				return err
			}
			_, err := io.WriteString(out, diskPackageXML)
			return err
		}},
		{"equal-size restored-mode mutation", e.CodeVerifyFailed, func(_ context.Context, path, _ string, out, _ io.Writer) error {
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("fake subject"), 0o600); err != nil {
				return err
			}
			if err := os.Chmod(path, 0o400); err != nil {
				return err
			}
			_, err := io.WriteString(out, diskPackageXML)
			return err
		}},
		{"malformed report", e.CodeEnvelopeDecode, func(_ context.Context, _, _ string, out, _ io.Writer) error {
			_, err := io.WriteString(out, "broken")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := heldfile.FromBytesContext(t.Context(), []byte("test subject"), heldfile.Options{Label: "disk-fixture", MaxBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			inventory, err := inspectHeldDiskPackages(t.Context(), held, "sha256:"+strings.Repeat("a", 64), tc.run)
			if tc.code != "" {
				assertDependencyCode(t, err, tc.code)
				if inventory != nil {
					t.Fatal("failed scan issued inventory")
				}
				return
			}
			if err != nil || len(inventory.Packages()) != 1 {
				t.Fatalf("inventory: %+v, %v", inventory, err)
			}
			digest, size, tool, _ := inventory.Binding()
			if digest != held.Digest() || size != held.Size() || tool != "sha256:"+strings.Repeat("a", 64) {
				t.Fatal("inventory is not bound to inspected bytes")
			}
			copy := inventory.Packages()
			copy[0].Name = "substituted"
			if inventory.Packages()[0].Name != "pkg" {
				t.Fatal("caller mutation changed inventory")
			}
		})
	}
}

func TestDiskInspectorAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, path, digest string
		code               e.Code
	}{
		{"unbounded tool", "/must-not-open", "invalid", e.CodeUnpinnedInput},
		{"relative executable", "must-not-open", "sha256:" + strings.Repeat("a", 64), e.CodeUsage},
		{"dirty absolute path", "/must-not-open/../tool", "sha256:" + strings.Repeat("a", 64), e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := OpenDiskInspector(t.Context(), tc.path, tc.digest, nil)
			assertDependencyCode(t, err, tc.code)
		})
	}
	_, err := OpenDiskInspector(t.Context(), "/must-not-open", "sha256:"+strings.Repeat("a", 64), nil)
	assertDependencyCode(t, err, e.CodeEngineCapability)
	var inspector DiskInspector
	for _, tc := range []struct {
		format, encryption string
		code               e.Code
	}{{"qcow2", "luks2", e.CodeEncryptedArtifact}, {"raw", "none", e.CodeEngineCapability}, {"qcow2", "none", e.CodeEngineCapability}} {
		_, err := inspector.InspectHeldInstalledPackages(t.Context(), nil, tc.format, tc.encryption)
		assertDependencyCode(t, err, tc.code)
	}
	if runtime.GOOS != "linux" {
		return
	}
	// A pinned script is not a held ELF authority. The codec is independently
	// held from an existing ELF; this test never runs it or claims a real scan.
	toolPath := "/usr/bin/true"
	raw, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := assemble.OpenQemuImgCodec(t.Context(), toolPath, rawDigest(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	script := filepath.Join(t.TempDir(), "inspector")
	data := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(script, data, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = OpenDiskInspector(t.Context(), script, rawDigest(data), codec)
	assertDependencyCode(t, err, e.CodeEngineCapability)
}
