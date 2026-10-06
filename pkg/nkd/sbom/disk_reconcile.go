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
	basis := make(map[diskObservedPackageKey]bool)
	materialFiles := make(map[string][]string)
	seenInputs := make(map[string]bool)
	for _, input := range inputs {
		if input == nil || !canonical.IsDigest(input.report.Subject) || input.report.Size <= 0 || input.runtimeDigest != root.runtimeDigest || input.report.Platform != root.report.Platform ||
			input.report.Scanner != root.report.Scanner || input.report.Guestfish != root.report.Guestfish || input.report.PackageReader != root.report.PackageReader || input.report.Codec != root.report.Codec || input.report.Inspector != root.report.Inspector || input.report.MaxFilesystemBytes != root.report.MaxFilesystemBytes {
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
		case "tarball", "host-package", "oci-image", "installer-content":
			staged = true
		default:
			return nil, e.New(e.CodeEngineCapability, "unsupported disk material observation kind %q", input.report.Kind)
		}
		for _, component := range input.report.Components {
			key, err := diskObservedComponentKey(component, root.report.Platform)
			if err != nil {
				return nil, err
			}
			if input.report.Kind == "oci-image" && key.class != diskClassContainer {
				return nil, e.New(e.CodeVerifyFailed, "OCI material has a substituted content class")
			}
			// Only installer filesystem observations or authoritative held
			// dpkg archive metadata may justify host Debian identities. A
			// staged scanner DB is not a package-reader observation.
			if key.format == "deb" && key.class == diskClassFilesystem && input.report.Kind != "installer-iso" {
				continue
			}
			basis[key] = true
			if key.class == diskClassDebianArchive {
				key.class = diskClassFilesystem
				basis[key] = true
			}
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
	for _, component := range root.report.Components {
		// An unknown version stays unknown in descriptive inventory. Equal
		// names and empty versions do not identify the same material bytes.
		if component.Version == "" {
			return nil, e.New(e.CodeProvenanceMissing, "disk package %q has no exact version for material correspondence", component.Name)
		}
		key, err := diskObservedComponentKey(component, root.report.Platform)
		if err != nil {
			return nil, err
		}
		if !basis[key] {
			return nil, e.New(e.CodeProvenanceMissing, "disk package %q version %q has no exact inspected material correspondence", component.Name, component.Version)
		}
		components = append(components, component)
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
	components, err = diskDocumentComponents(components)
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

const (
	diskContentClassProperty = "nkd:diskContentClass"
	diskClassFilesystem      = "filesystem"
	diskClassContainer       = "container"
	diskClassDebianArchive   = "debian-archive"
)

type diskObservedPackageKey struct {
	diskPackageKey
	class string
}

// This class describes the actual observation operation, not installed origin.
// Native scanner fields are projected separately; they cannot author this key.
func withDiskContentClass(components []Component, class string) []Component {
	result := make([]Component, len(components))
	for index, component := range components {
		result[index] = cloneComponent(component)
		result[index].Properties = append(result[index].Properties, Property{Name: diskContentClassProperty, Value: class})
		slices.SortFunc(result[index].Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
	}
	return result
}

func diskComponentClass(component Component) (string, error) {
	class := ""
	seen := false
	for _, property := range component.Properties {
		if property.Name != diskContentClassProperty {
			continue
		}
		if seen {
			return "", e.New(e.CodeEnvelopeDecode, "disk component has ambiguous observation classes")
		}
		seen = true
		class = property.Value
	}
	switch class {
	case diskClassFilesystem, diskClassContainer, diskClassDebianArchive:
		return class, nil
	case "":
		return "", e.New(e.CodeProvenanceMissing, "disk component has no owner-issued observation class")
	default:
		return "", e.New(e.CodeEnvelopeDecode, "disk component has an unsupported observation class")
	}
}

func diskObservedComponentKey(component Component, platform string) (diskObservedPackageKey, error) {
	key, err := diskComponentKey(component, platform)
	if err != nil {
		return diskObservedPackageKey{}, err
	}
	class, err := diskComponentClass(component)
	if err != nil {
		return diskObservedPackageKey{}, err
	}
	if class == diskClassDebianArchive && key.format != "deb" {
		return diskObservedPackageKey{}, e.New(e.CodeEnvelopeDecode, "package-reader observation is not Debian metadata")
	}
	return diskObservedPackageKey{diskPackageKey: key, class: class}, nil
}

// Class-sensitive checks precede de-duplication. Public CycloneDX package refs
// remain unchanged: this private receipt classifier is not package provenance
// and cannot create conflicting public properties for a host/container overlap.
func diskDocumentComponent(component Component) Component {
	result := cloneComponent(component)
	result.Properties = slices.DeleteFunc(result.Properties, func(property Property) bool { return property.Name == diskContentClassProperty })
	return result
}

// Existing public package refs combine repeated observations. Preserve every
// immediate carrier before that deduplication as a deterministic descriptive
// string list encoded by the existing canonical codec. No carrier is parsed or
// used as authority, and conflicting non-containment metadata still fails in
// WithDependencies rather than being silently merged here.
func diskDocumentComponents(observed []Component) ([]Component, error) {
	carriers := make(map[string][]string)
	for _, component := range observed {
		for _, property := range component.Properties {
			if property.Name == "nkd:containedIn" {
				ref := dependencyRef(component)
				carriers[ref] = append(carriers[ref], property.Value)
			}
		}
	}
	result := make([]Component, len(observed))
	for index, component := range observed {
		component = diskDocumentComponent(component)
		if values := carriers[dependencyRef(component)]; len(values) > 0 {
			values = slices.Clone(values)
			slices.Sort(values)
			values = slices.Compact(values)
			raw, err := canonical.Marshal(values)
			if err != nil {
				return nil, e.Wrap(e.CodeInternal, err, "encoding descriptive archive containment")
			}
			component.Properties = slices.DeleteFunc(component.Properties, func(property Property) bool { return property.Name == "nkd:containedIn" })
			component.Properties = append(component.Properties, Property{Name: "nkd:containedIn", Value: string(raw)})
			slices.SortFunc(component.Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
		}
		result[index] = component
	}
	return result, nil
}

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
	components, err = diskDocumentComponents(components)
	if err != nil {
		return nil, err
	}
	bom, err = WithDependencies(bom, components, "disk-content/v1", inspection.report.Scanner)
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
	if key.name == "" || key.format == "" || (key.format == "deb" && key.version == "") {
		return diskPackageKey{}, e.New(e.CodeProvenanceMissing, "disk package lacks an exact semantic identity")
	}
	if key.format == "deb" && key.architecture != "all" && key.architecture != strings.TrimPrefix(platform, "linux/") {
		return diskPackageKey{}, e.New(e.CodeVerifyFailed, "disk Debian package has a missing or different consumer architecture")
	}
	return key, nil
}
