package sbom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type cancelDiskInspectionCapture struct {
	cancel context.CancelFunc
}

func (reader cancelDiskInspectionCapture) Read(raw []byte) (int, error) {
	reader.cancel()
	raw[0] = 'x'
	return 1, nil
}

func TestDiskInspectionCaptureDistinguishesCancellationAndIntegrity(t *testing.T) {
	digest := sha256.Sum256([]byte("x"))
	pin := "sha256:" + hex.EncodeToString(digest[:])
	for _, tt := range []struct {
		name   string
		input  string
		digest string
		size   int64
		cancel bool
		want   e.Code
	}{
		{name: "exact", input: "x", digest: pin, size: 1},
		{name: "digest mismatch", input: "y", digest: pin, size: 1, want: e.CodeVerifyFailed},
		{name: "size mismatch", input: "x", digest: pin, size: 2, want: e.CodeVerifyFailed},
		{name: "cancel during capture", digest: pin, size: 1, cancel: true, want: e.CodeTimeout},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var input io.Reader = strings.NewReader(tt.input)
			if tt.cancel {
				input = cancelDiskInspectionCapture{cancel: cancel}
			}
			subject, err := holdDiskInspectionSubject(ctx, input, tt.digest, tt.size)
			if sbomCode(err) != tt.want || (subject != nil) != (tt.want == "") {
				t.Fatalf("subject=%v error=%v, want %s", subject, err, tt.want)
			}
			if subject != nil {
				_ = subject.Close()
			}
		})
	}
}

func TestDiskInspectionRuntimeRejectsDescriptions(t *testing.T) {
	for _, observation := range []*pipeline.DiskInspectionObservation{nil, {}} {
		if inventory, err := inventoryFromContainerObservation(observation); inventory != nil || sbomCode(err) == "" {
			t.Fatalf("unissued observation accepted: %v", err)
		}
	}
	if (&DiskInstalledInventory{}).RuntimeBinding() != "" {
		t.Fatal("executable-only inventory grants runtime binding")
	}
	var capability *DiskInspectionRuntime
	if _, _, _, err := capability.BindingContext(t.Context()); sbomCode(err) != e.CodeEngineCapability {
		t.Fatalf("absent runtime: %v", err)
	}
	if err := capability.AdmitSubject(t.Context(), 1); sbomCode(err) != e.CodeEngineCapability {
		t.Fatalf("absent runtime admits subject: %v", err)
	}
	if err := capability.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestContainerInstalledPackagesCheckBindingAndMetadata(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	valid := diskInspectionReport{Subject: digest, Size: 123, Inspector: digest, Codec: digest,
		Packages: []InstalledDiskPackage{{Name: "hello", Version: "1", Architecture: "arm64", Format: "deb"}}}
	for _, tt := range []struct {
		name  string
		edit  func(*diskInspectionReport)
		image string
		want  e.Code
	}{
		{name: "valid"},
		{name: "wrong subject", edit: func(r *diskInspectionReport) { r.Subject = "sha256:" + strings.Repeat("2", 64) }, want: e.CodeVerifyFailed},
		{name: "wrong size", edit: func(r *diskInspectionReport) { r.Size++ }, want: e.CodeVerifyFailed},
		{name: "wrong inspector", edit: func(r *diskInspectionReport) { r.Inspector = "" }, want: e.CodeVerifyFailed},
		{name: "wrong codec", edit: func(r *diskInspectionReport) { r.Codec = "" }, want: e.CodeVerifyFailed},
		{name: "invalid closure", image: "tag", want: e.CodeVerifyFailed},
		{name: "empty inventory", edit: func(r *diskInspectionReport) { r.Packages = nil }, want: e.CodeVerifyFailed},
		{name: "excess package count", edit: func(r *diskInspectionReport) { r.Packages = make([]InstalledDiskPackage, maxDiskInstalledPackages+1) }, want: e.CodeVerifyFailed},
		{name: "empty name", edit: func(r *diskInspectionReport) { r.Packages[0].Name = "" }, want: e.CodeEnvelopeDecode},
		{name: "control characters", edit: func(r *diskInspectionReport) { r.Packages[0].Name = "hello\x1b[31m" }, want: e.CodeEnvelopeDecode},
		{name: "oversized field", edit: func(r *diskInspectionReport) { r.Packages[0].Version = strings.Repeat("x", 4097) }, want: e.CodeEnvelopeDecode},
		{name: "unsupported format", edit: func(r *diskInspectionReport) { r.Packages[0].Format = "rpm" }, want: e.CodeEngineCapability},
		{name: "conflicting duplicate", edit: func(r *diskInspectionReport) {
			r.Packages = append(r.Packages, InstalledDiskPackage{Name: "hello", Version: "2", Architecture: "arm64", Format: "deb"})
		}, want: e.CodeVerifyFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report := valid
			report.Packages = append([]InstalledDiskPackage(nil), valid.Packages...)
			if tt.edit != nil {
				tt.edit(&report)
			}
			raw, err := canonical.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			image := digest
			if tt.image != "" {
				image = tt.image
			}
			packages, err := checkedContainerInstalledPackages(raw, digest, 123, image, digest, digest)
			if sbomCode(err) != tt.want {
				t.Fatalf("report error %v, want %s", err, tt.want)
			}
			if tt.want == "" && (len(packages) != 1 || packages[0] != valid.Packages[0]) {
				t.Fatalf("valid inventory changed: %v", packages)
			}
		})
	}
	raw, err := canonical.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	if _, err := checkedContainerInstalledPackages(raw, digest, 123, digest, digest, digest); sbomCode(err) != e.CodeEnvelopeDecode {
		t.Fatalf("unknown report field accepted: %v", err)
	}
	packages := []InstalledDiskPackage{{Name: "z", Format: "deb"}, {Name: "a", Format: "deb"}, {Name: "a", Format: "deb"}}
	got, err := checkedInstalledDiskPackages(packages)
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Name != "z" || packages[0].Name != "z" {
		t.Fatalf("normalization changed input or did not sort/compact: %v, %v", got, err)
	}
}

func TestDiskInspectionHelperAdmitsBeforeToolIO(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	for _, tt := range []struct {
		name                      string
		ctx                       context.Context
		inspector, codec, subject string
		size                      int64
		input                     io.Reader
		output                    io.Writer
		want                      e.Code
	}{
		{name: "nil context", inspector: digest, codec: digest, subject: digest, size: 1, input: strings.NewReader("x"), output: io.Discard, want: e.CodeUsage},
		{name: "wrong inspector", ctx: t.Context(), codec: digest, subject: digest, size: 1, input: strings.NewReader("x"), output: io.Discard, want: e.CodeUnpinnedInput},
		{name: "wrong codec", ctx: t.Context(), inspector: digest, subject: digest, size: 1, input: strings.NewReader("x"), output: io.Discard, want: e.CodeUnpinnedInput},
		{name: "oversized subject", ctx: t.Context(), inspector: digest, codec: digest, subject: digest, size: MaxDiagnosticDiskBytes + 1, input: strings.NewReader("x"), output: io.Discard, want: e.CodeUnpinnedInput},
		{name: "missing input", ctx: t.Context(), inspector: digest, codec: digest, subject: digest, size: 1, output: io.Discard, want: e.CodeUsage},
		{name: "missing output", ctx: t.Context(), inspector: digest, codec: digest, subject: digest, size: 1, input: strings.NewReader("x"), want: e.CodeUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := RunDiskInspectionHelper(tt.ctx, tt.inspector, tt.codec, tt.subject, tt.size, tt.input, tt.output); sbomCode(err) != tt.want {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
}
