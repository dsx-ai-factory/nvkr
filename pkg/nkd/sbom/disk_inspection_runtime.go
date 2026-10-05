package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// DiskInspectionRuntime admits the entire local OCI inspection closure and its
// concrete sandbox, rather than accepting callers' isolation promises. Factory
// remains the owner of the original output/Plan/Run binding and live lifetime.
type DiskInspectionRuntime struct {
	container *pipeline.DiskInspectionContainer
}

// OpenDiskInspectionRuntime checks an already-local exact image and observes
// the caller-selected trusted Linux runtime before touching disk input/output.
// The runtime is held and rechecked for freshness, not authenticated against an
// independently supplied executable or daemon pin.
func OpenDiskInspectionRuntime(ctx context.Context, selection pipeline.DiskInspectionSelection) (*DiskInspectionRuntime, error) {
	container, err := pipeline.OpenDiskInspectionContainer(ctx, selection)
	if err != nil {
		return nil, err
	}
	return &DiskInspectionRuntime{container: container}, nil
}

func (runtime *DiskInspectionRuntime) BindingContext(ctx context.Context) (image, inspector, codec string, err error) {
	if runtime == nil {
		return "", "", "", e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	return runtime.container.BindingContext(ctx)
}

func (runtime *DiskInspectionRuntime) Close() error {
	if runtime == nil {
		return nil
	}
	return runtime.container.Close()
}

// InspectHeldInstalledPackages accepts only held subject authority. The fixed
// container helper uses the existing exact held probe/inspector boundary; this
// owner authenticates its returned metadata against the opaque container result.
func (runtime *DiskInspectionRuntime) InspectHeldInstalledPackages(ctx context.Context, subject *heldfile.File, format, encryption string) (*DiskInstalledInventory, error) {
	if err := validateContainerInspection(ctx, format, encryption); err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	observation, err := runtime.container.InspectHeld(ctx, subject)
	if err != nil {
		return nil, err
	}
	return inventoryFromContainerObservation(observation)
}

// InspectPackerInstalledPackages owns before-copy admission, inspection and
// temporary snapshot cleanup. Only the original opaque Packer artifact enters
// this operation; neither its snapshot nor its capacity permit escapes.
func (runtime *DiskInspectionRuntime) InspectPackerInstalledPackages(ctx context.Context, artifact *pipeline.PackerArtifact, format, encryption string) (*DiskInstalledInventory, error) {
	if err := validateContainerInspection(ctx, format, encryption); err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	observation, err := runtime.container.InspectPackerArtifact(ctx, artifact)
	if err != nil {
		return nil, err
	}
	return inventoryFromContainerObservation(observation)
}

func validateContainerInspection(ctx context.Context, format, encryption string) error {
	if err := validateSBOMContext(ctx); err != nil {
		return err
	}
	if encryption != "none" {
		return e.New(e.CodeEncryptedArtifact, "disk inspection requires unencrypted content")
	}
	if format != "qcow2" {
		return e.New(e.CodeEngineCapability, "disk inspection supports only QCOW2")
	}
	return nil
}

type diskInspectionReport struct {
	Subject   string                 `json:"subject"`
	Size      int64                  `json:"size"`
	Inspector string                 `json:"inspector"`
	Codec     string                 `json:"codec"`
	Packages  []InstalledDiskPackage `json:"packages"`
}

func inventoryFromContainerObservation(observation *pipeline.DiskInspectionObservation) (*DiskInstalledInventory, error) {
	if observation == nil {
		return nil, e.New(e.CodeVerifyFailed, "disk inspection observation is absent")
	}
	raw := observation.Report()
	subject, size, image, inspector, codec := observation.Binding()
	packages, err := checkedContainerInstalledPackages(raw, subject, size, image, inspector, codec)
	if err != nil {
		return nil, err
	}
	return &DiskInstalledInventory{subjectDigest: subject, subjectSize: size, inspectorDigest: inspector, codecDigest: codec, runtimeDigest: image, packages: packages}, nil
}

// checkedContainerInstalledPackages validates untrusted helper metadata without
// issuing authority. Only the opaque container observation above can do that.
func checkedContainerInstalledPackages(raw []byte, subject string, size int64, image, inspector, codec string) ([]InstalledDiskPackage, error) {
	if len(raw) > maxDiskPackageReportBytes {
		return nil, e.New(e.CodeEnvelopeDecode, "disk inspection report exceeds its budget")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "validating inspection helper report")
	}
	var report diskInspectionReport
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return nil, e.Wrap(e.CodeEnvelopeDecode, err, "decoding inspection helper report")
	}
	if !canonical.IsDigest(image) || report.Subject != subject || report.Size != size || report.Inspector != inspector || report.Codec != codec || len(report.Packages) == 0 || len(report.Packages) > maxDiskInstalledPackages {
		return nil, e.New(e.CodeVerifyFailed, "inspection helper report differs from its held subject or tool binding")
	}
	// The helper parses attacker-controlled disk contents. Reapply the owning
	// metadata invariants outside the sandbox before accepting its report.
	return checkedInstalledDiskPackages(report.Packages)
}

// RunDiskInspectionHelper is the thin container entrypoint's owning operation.
// It accepts exact stdin bytes, pins both Linux ELF tools and uses the existing
// held-envelope gate before libguestfs. No guest executable is ever run.
func RunDiskInspectionHelper(ctx context.Context, inspectorDigest, codecDigest, subjectDigest string, size int64, input io.Reader, output io.Writer) error {
	if err := validateSBOMContext(ctx); err != nil {
		return err
	}
	if size <= 0 || size > MaxDiagnosticDiskBytes || !canonical.IsDigest(subjectDigest) || !canonical.IsDigest(inspectorDigest) || !canonical.IsDigest(codecDigest) {
		return e.New(e.CodeUnpinnedInput, "inspection helper requires a bounded exact subject")
	}
	if input == nil || output == nil {
		return e.New(e.CodeUsage, "inspection helper requires input and output streams")
	}
	ctx, cancel := context.WithTimeout(ctx, pipeline.MaxDiskInspectionTimeout)
	defer cancel()
	codec, err := assemble.OpenQemuImgCodec(ctx, "/usr/bin/qemu-img", codecDigest)
	if err != nil {
		return err
	}
	defer func() { _ = codec.Close() }()
	inspector, err := OpenDiskInspector(ctx, "/usr/bin/virt-inspector", inspectorDigest, codec)
	if err != nil {
		return err
	}
	defer func() { _ = inspector.Close() }()
	subject, err := holdDiskInspectionSubject(ctx, input, subjectDigest, size)
	if err != nil {
		return err
	}
	defer func() { _ = subject.Close() }()
	inventory, err := inspector.InspectHeldInstalledPackages(ctx, subject, "qcow2", "none")
	if err != nil {
		return err
	}
	report := diskInspectionReport{Subject: subject.Digest(), Size: subject.Size(), Inspector: inspectorDigest, Codec: codecDigest, Packages: inventory.Packages()}
	raw, err := canonical.Marshal(report)
	if err != nil {
		return err
	}
	if len(raw) > maxDiskPackageReportBytes {
		return e.New(e.CodeEnvelopeDecode, "inspection helper report exceeds its budget")
	}
	// Do not emit an accepted report before owned capabilities have closed. The
	// enclosing adapter still waits for process exit and container cleanup.
	if err := errors.Join(inspector.Close(), codec.Close(), subject.Close()); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing inspection helper capabilities")
	}
	if _, err := output.Write(raw); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing inspection report")
	}
	return nil
}

func holdDiskInspectionSubject(ctx context.Context, input io.Reader, digest string, size int64) (*heldfile.File, error) {
	subject, err := heldfile.FromReaderContext(ctx, input, heldfile.Options{Label: "inspection-subject", MaxBytes: size, ExpectedDigest: digest})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, errors.Join(err, ctx.Err()), "holding container inspection subject")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding container inspection subject")
	}
	if subject.Size() != size {
		_ = subject.Close()
		return nil, e.New(e.CodeVerifyFailed, "inspection subject size differs")
	}
	return subject, nil
}
