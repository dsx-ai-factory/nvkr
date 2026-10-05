package sbom

import (
	"context"
	"slices"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// ReconcileDiskContents produces a descriptive exact-subject document from
// package-issued content observations. Factory must additionally bind these
// observations to its original live producer graph and acquisition carrier.
// This operation grants neither publication nor complete source provenance.
func ReconcileDiskContents(ctx context.Context, buildDigest, name string, root *DiskContentInspection, inputs []*DiskContentInspection) (*BOM, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	if root == nil || root.report.Kind != "disk-image" || !canonical.IsDigest(root.runtimeDigest) || !canonical.IsDigest(buildDigest) {
		return nil, e.New(e.CodeProvenanceMissing, "disk reconciliation requires issued exact subject observations")
	}
	if err := validateDiskContentReport(root.report); err != nil {
		return nil, err
	}
	base, capture, staged := false, false, false
	basis := make(map[diskPackageKey]bool)
	materialFiles := make(map[string][]string)
	seenInputs := make(map[string]bool)
	for _, input := range inputs {
		if input == nil || !canonical.IsDigest(input.report.Subject) || input.report.Size <= 0 || input.runtimeDigest != root.runtimeDigest || input.report.Platform != root.report.Platform ||
			input.report.Scanner != root.report.Scanner || input.report.Guestfish != root.report.Guestfish || input.report.PackageReader != root.report.PackageReader || input.report.Codec != root.report.Codec || input.report.Inspector != root.report.Inspector {
			return nil, e.New(e.CodeVerifyFailed, "disk material content differs from its exact inspection closure")
		}
		if seenInputs[input.report.Subject] {
			return nil, e.New(e.CodeVerifyFailed, "disk reconciliation repeats an input subject")
		}
		seenInputs[input.report.Subject] = true
		if err := validateDiskContentReport(input.report); err != nil {
			return nil, err
		}
		switch input.report.Kind {
		case "installer-iso":
			base = true
		case "apt-acquisition":
			capture = true
		case "tarball", "host-package", "oci-image":
			staged = true
		default:
			return nil, e.New(e.CodeEngineCapability, "unsupported disk material observation kind %q", input.report.Kind)
		}
		for _, component := range input.report.Components {
			key, err := diskComponentKey(component, root.report.Platform)
			if err != nil {
				return nil, err
			}
			basis[key] = true
		}
		for _, file := range input.report.Files {
			if file.Kind == "regular" {
				materialFiles[file.Digest] = append(materialFiles[file.Digest], input.report.Subject)
			}
		}
	}
	if !base || !capture || !staged {
		return nil, e.New(e.CodeProvenanceMissing, "disk reconciliation requires inspected base installer, retained acquisitions and staged component payloads")
	}
	var components []Component
	seenPackages := make(map[diskPackageKey]bool)
	for _, component := range root.report.Components {
		key, err := diskComponentKey(component, root.report.Platform)
		if err != nil {
			return nil, err
		}
		if !basis[key] {
			return nil, e.New(e.CodeProvenanceMissing, "disk package %q version %q has no exact inspected material correspondence", component.Name, component.Version)
		}
		if seenPackages[key] {
			continue
		}
		seenPackages[key] = true
		components = append(components, cloneComponent(component))
	}
	// Include every observed regular file, including loose binaries which package
	// catalogers do not recognize. A material hash match describes correspondence;
	// absence is not mislabeled as an origin, package ownership or derivation proof.
	for _, file := range root.report.Files {
		if file.Kind != "regular" {
			continue
		}
		component := Component{Type: "file", Name: file.Path, Hashes: []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(file.Digest, "sha256:")}}}
		if subjects := materialFiles[file.Digest]; len(subjects) > 0 {
			subjects = slices.Clone(subjects)
			slices.Sort(subjects)
			subjects = slices.Compact(subjects)
			component.Properties = []Property{{Name: "nkd:materialSubjects", Value: strings.Join(subjects, ",")}}
		}
		components = append(components, component)
	}
	bom, err := NewSubject(buildDigest, name, root.report.Subject)
	if err != nil {
		return nil, err
	}
	bom, err = WithDependencies(bom, components, "disk-reconciled/v1", root.report.Scanner)
	if err != nil {
		return nil, err
	}
	bom.Metadata.Props = append(bom.Metadata.Props,
		Property{Name: "nkd:diskInspectionRuntimeDigest", Value: root.runtimeDigest}, Property{Name: "nkd:diskInspectorDigest", Value: root.report.Inspector},
		Property{Name: "nkd:diskCodecDigest", Value: root.report.Codec})
	slices.SortFunc(bom.Metadata.Props, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
	return ForSubject(bom, root.report.Subject, "build-output", "")
}

type diskPackageKey struct{ name, version, format, architecture string }

// DiskContentDocument projects already-issued material discovery onto its exact
// existing build-output subject. It is descriptive and cannot authorize a Run.
func DiskContentDocument(buildDigest, name string, inspection *DiskContentInspection) (*BOM, error) {
	if inspection == nil || !canonical.IsDigest(inspection.runtimeDigest) {
		return nil, e.New(e.CodeProvenanceMissing, "component inventory has no issued inspection")
	}
	if err := validateDiskContentReport(inspection.report); err != nil {
		return nil, err
	}
	bom, err := NewSubject(buildDigest, name, inspection.report.Subject)
	if err != nil {
		return nil, err
	}
	components := inspection.Components()
	for _, file := range inspection.report.Files {
		if file.Kind == "regular" {
			components = append(components, Component{Type: "file", Name: file.Path, Hashes: []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(file.Digest, "sha256:")}}})
		}
	}
	bom, err = WithDependencies(bom, components, "syft-cyclonedx/v1", inspection.report.Scanner)
	if err != nil {
		return nil, err
	}
	return ForSubject(bom, inspection.report.Subject, "build-output", "")
}

// WithSourceSubject retains the existing source-bound per-artifact subject
// without replacing inspected dependency/file evidence. Both exact subjects and
// Build bindings must already agree; declarations cannot add inspected content.
func WithSourceSubject(document, source *BOM) (*BOM, error) {
	if err := document.Validate(); err != nil {
		return nil, err
	}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	documentBuild, documentArtifact, err := metadataBindings(document.Metadata.Props)
	if err != nil {
		return nil, err
	}
	sourceBuild, sourceArtifact, err := metadataBindings(source.Metadata.Props)
	if err != nil {
		return nil, err
	}
	if documentBuild != sourceBuild || documentArtifact != sourceArtifact || len(source.Metadata.Component.Hashes) != 1 || !slices.Equal(document.Metadata.Component.Hashes, source.Metadata.Component.Hashes) {
		return nil, e.New(e.CodeVerifyFailed, "source declaration differs from inspected component subject")
	}
	result := cloneBOM(document)
	reference := result.Metadata.Component.BOMRef
	result.Metadata.Component = cloneComponent(source.Metadata.Component)
	result.Metadata.Component.BOMRef = reference
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

// Values are exactly those reported by pinned Syft/dpkg-deb. In particular the
// Debian version is never shortened, parsed or compared with a made-up grammar.
func diskComponentKey(component Component, platform string) (diskPackageKey, error) {
	key := diskPackageKey{name: component.Name, version: component.Version}
	for _, property := range component.Properties {
		switch property.Name {
		case "nkd:packageFormat":
			key.format = property.Value
		case "nkd:packageArchitecture":
			key.architecture = property.Value
		}
	}
	if key.name == "" || key.version == "" || key.format == "" {
		return diskPackageKey{}, e.New(e.CodeProvenanceMissing, "disk package lacks an exact semantic identity")
	}
	if key.format == "deb" && key.architecture != "all" && key.architecture != strings.TrimPrefix(platform, "linux/") {
		return diskPackageKey{}, e.New(e.CodeVerifyFailed, "disk Debian package has a missing or different consumer architecture")
	}
	return key, nil
}
