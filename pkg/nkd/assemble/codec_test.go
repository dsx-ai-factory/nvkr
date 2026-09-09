package assemble

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func code(err error) nkderr.Code {
	if coded, ok := err.(*nkderr.Error); ok {
		return coded.Code
	}
	return ""
}

func fakeQemuImg(t *testing.T, infoJSON string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qemu-img")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = info ]; then " +
		"if [ -n \"${NKD_RENAME_RESTORE_ENVELOPE:-}\" ]; then " +
		"mv \"$NKD_RENAME_RESTORE_ENVELOPE\" \"$NKD_RENAME_RESTORE_ENVELOPE.before-attack\"; " +
		"printf 'mutated-after-snapshot' > \"$NKD_RENAME_RESTORE_ENVELOPE\"; " +
		"if [ -n \"${NKD_ENVELOPE_ATTACK_MARKER:-}\" ]; then : > \"$NKD_ENVELOPE_ATTACK_MARKER\"; fi; fi; " +
		"printf '%s' '" + infoJSON + "'; exit 0; fi\n" +
		"if [ \"$1\" = convert ]; then cp \"$6\" \"$7\"; result=$?; " +
		"if [ -n \"${NKD_RENAME_RESTORE_ENVELOPE:-}\" ] && [ -e \"$NKD_RENAME_RESTORE_ENVELOPE.before-attack\" ]; then " +
		"rm -f \"$NKD_RENAME_RESTORE_ENVELOPE\"; mv \"$NKD_RENAME_RESTORE_ENVELOPE.before-attack\" \"$NKD_RENAME_RESTORE_ENVELOPE\"; fi; " +
		"exit $result; fi\n" +
		"exit 9\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, _, err := LogicalDiskDigestContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return path, digest
}

func TestQualifiedCodecSnapshotsAndPinsExecutable(t *testing.T) {
	qemu, digest := fakeQemuImg(t, `{"format":"qcow2","virtual-size":12,"encrypted":false}`)
	envelope := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(envelope, []byte("hello world!"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongPinCode := nkderr.CodeUnverifiedRef
	if runtime.GOOS != "linux" {
		// Executable holding is a platform capability prerequisite. Darwin has no
		// descriptor-only executable primitive, so it must fail before evaluating
		// even a deliberately wrong content pin.
		wrongPinCode = nkderr.CodeEngineCapability
	}
	if _, err := OpenQemuImgCodec(context.Background(), qemu,
		"sha256:"+strings.Repeat("f", 64)); code(err) != wrongPinCode {
		t.Fatalf("wrong executable pin returned %v, want %s", err, wrongPinCode)
	}
	codec, err := OpenQemuImgCodec(context.Background(), qemu, digest)
	if runtime.GOOS != "linux" {
		if code(err) != nkderr.CodeEngineCapability {
			t.Fatalf("held qemu-img execution must fail closed on %s, got %v", runtime.GOOS, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	backup := qemu + ".before-attack"
	if err := os.Rename(qemu, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qemu, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, size, err := codec.EnvelopeLogicalDigest(context.Background(), "qcow2", envelope)
	if err != nil {
		t.Fatal(err)
	}
	want, _, _ := LogicalDiskDigestContext(context.Background(), envelope)
	if got != want || size != 12 {
		t.Fatalf("decoded identity = %s/%d, want %s/12", got, size, want)
	}
	if err := os.Remove(qemu); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, qemu); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.executable.OSFile().WriteAt([]byte("X"), 0); err == nil {
		t.Fatal("held qemu-img executable accepted a write")
	}
	if err := codec.executable.AssertUnchanged(); err != nil {
		t.Fatalf("rejected write changed held executable: %v", err)
	}
}

func TestQualifiedCodecRejectsEncryptedBackingAndFormatConfusion(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("qualified descriptor execution is Linux-only")
	}
	envelope := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(envelope, []byte("hello world!"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, info := range map[string]string{
		"encrypted": `{"format":"qcow2","virtual-size":12,"encrypted":true}`,
		"backing":   `{"format":"qcow2","virtual-size":12,"backing-filename":"base.raw"}`,
		"format":    `{"format":"vmdk","virtual-size":12}`,
	} {
		t.Run(name, func(t *testing.T) {
			qemu, digest := fakeQemuImg(t, info)
			codec, err := OpenQemuImgCodec(context.Background(), qemu, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer codec.Close()
			_, _, err = codec.EnvelopeLogicalDigest(context.Background(), "qcow2", envelope)
			if name == "encrypted" {
				if code(err) != nkderr.CodeEncryptedArtifact {
					t.Fatalf("encrypted envelope code = %q (%v)", code(err), err)
				}
			} else if code(err) != nkderr.CodeEnvelopeDecode {
				t.Fatalf("invalid envelope code = %q (%v)", code(err), err)
			}
		})
	}
}

func TestCodecFailClosedBoundaries(t *testing.T) {
	if _, err := OpenQemuImgCodec(nil, "qemu-img", ""); code(err) != nkderr.CodeUsage { //nolint:staticcheck // verifies the nil-context fail-closed contract
		t.Fatalf("nil context code = %q (%v)", code(err), err)
	}
	var codec QemuImgCodec
	if _, _, err := codec.EnvelopeLogicalDigest(context.Background(), "raw", "must-not-open"); code(err) != nkderr.CodeEnvelopeDecode {
		t.Fatalf("raw is not a derived envelope format: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenQemuImgCodec(ctx, "qemu-img", "sha256:"+strings.Repeat("a", 64)); code(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled open code = %q (%v)", code(err), err)
	}
}
