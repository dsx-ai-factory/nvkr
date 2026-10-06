package factory

import (
	"context"
	"path/filepath"
	"reflect"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// The frozen root execution binding owns native inspection tooling selection.
// The guest/output platform remains independent (for example a TCG cross-build).
func diskPublicationInspectionProfile(build BuildPlan, environment definition.Environment) (string, definition.DiskInspectionProfile, error) {
	root, err := buildgraph.DiskRootName(build.ResolvedComponents)
	if err != nil {
		return "", definition.DiskInspectionProfile{}, err
	}
	if build.Graph != nil {
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindBuild || node.Component != root {
				continue
			}
			if node.ExecutionBinding == nil || string(node.ExecutionBinding.Class.HostPlatform) != node.ToolchainPlatform {
				return "", definition.DiskInspectionProfile{}, e.New(e.CodePipelineDrift, "disk inspection lacks its frozen root execution host")
			}
			host := string(node.ExecutionBinding.Class.HostPlatform)
			profile, ok := environment.DiskInspectionProfiles[host]
			if !ok {
				return "", definition.DiskInspectionProfile{}, e.New(e.CodeEngineCapability, "disk output %q requires its frozen execution-host inspection profile", build.Name)
			}
			if err := profile.Validate(host); err != nil {
				return "", definition.DiskInspectionProfile{}, err
			}
			return host, profile, nil
		}
	}
	return "", definition.DiskInspectionProfile{}, e.New(e.CodePipelineDrift, "disk inspection has no frozen root producer")
}

// validateReconciledDiskDocument checks persisted document membership and exact
// planned inspection pins. It authenticates no receipt; the producer calls it
// only after live export, and read-back remains subject to normal signing gates.
func validateReconciledDiskDocument(plan *Plan, output, component, subject string, bom *sbom.BOM) error {
	if err := bom.Validate(); err != nil {
		return err
	}
	_, artifacts := planIndex(plan)
	var expected LifecycleOutputPlan
	var platform string
	var profile definition.DiskInspectionProfile
	var found bool
	var sourceComponent *catalog.NamedComponent
	for _, binding := range plan.LifecycleOutputs {
		if binding.Name == output && binding.Kind == PackageDiskImage {
			expected = binding
		}
	}
	for _, build := range plan.Builds {
		if build.Name == output && build.BuildDigest == expected.BuildDigest {
			platform = build.Platform
			_, selected, err := diskPublicationInspectionProfile(build, plan.Environment)
			if err != nil {
				return err
			}
			profile, found = selected, true
			for index := range build.ResolvedComponents {
				if build.ResolvedComponents[index].Name == component {
					sourceComponent = &build.ResolvedComponents[index]
				}
			}
		}
	}
	if !found || !containsString(expected.Components, component) || len(bom.Components) == 0 ||
		bom.Metadata.Component.Name != component || len(bom.Metadata.Component.Hashes) != 1 ||
		"sha256:"+bom.Metadata.Component.Hashes[0].Content != subject {
		return e.New(e.CodeProvenanceMissing, "disk evidence omits a complete exact component inventory")
	}
	if sourceComponent == nil {
		return e.New(e.CodeProvenanceMissing, "disk evidence lacks its exact resolved component source")
	}
	// Reuse the canonical source owner used at receipt issuance. This binds the
	// persisted subject's full source/prebuilt lane, not merely its name/hash.
	sources, _, err := sbom.PerArtifact(&buildgraph.Resolved{Platform: platform,
		Components: []catalog.NamedComponent{*sourceComponent}}, expected.BuildDigest, map[string]string{component: subject})
	if err != nil {
		return err
	}
	source := sources[component]
	if source == nil {
		return e.New(e.CodeProvenanceMissing, "disk evidence has no source-bound component subject")
	}
	root := artifacts[expected.ArtifactID]
	if component == root.Component {
		// The reconciliation owner projects the source fields while retaining
		// the inspected OS subject type. Material subjects still compare against
		// their original producer type, not a caller-selected replacement.
		source, err = sbom.WithSourceSubject(bom, source)
		if err != nil {
			return err
		}
	}
	actual := bom.Metadata.Component
	actual.BOMRef = source.Metadata.Component.BOMRef
	if !reflect.DeepEqual(actual, source.Metadata.Component) {
		return e.New(e.CodeVerifyFailed, "disk evidence changes its frozen full component source subject")
	}
	properties := make(map[string]string, len(bom.Metadata.Props))
	for _, property := range bom.Metadata.Props {
		properties[property.Name] = property.Value
	}
	method := "disk-content/v1"
	if component == root.Component {
		method = "disk-reconciled/v1"
		if !canonical.IsDigest(properties["nkd:diskInspectionRuntimeDigest"]) ||
			properties["nkd:diskInspectorDigest"] != profile.InspectorDigest || properties["nkd:diskCodecDigest"] != profile.CodecDigest {
			return e.New(e.CodeVerifyFailed, "root disk evidence differs from planned inspector or codec")
		}
	}
	if properties["nkd:buildDigest"] != expected.BuildDigest || properties["nkd:artifact"] != component ||
		properties["nkd:evidenceStage"] != "build-output" || properties["nkd:dependencyInspection"] != method ||
		properties["nkd:dependencyScannerDigest"] != profile.ScannerDigest {
		return e.New(e.CodeVerifyFailed, "disk component evidence differs from exact build, stage or scanner")
	}
	return nil
}

type reconciledEvidenceFiles struct {
	directory string
	files     map[string][]byte
}

// prepareReconciledEvidence admits every canonical document and exact producer
// record before package output construction. The returned bytes are descriptions
// of an already-authenticated export, never a substitute for that export.
func prepareReconciledEvidence(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, root *sbom.BOM,
	documents map[string]*sbom.BOM,
) (reconciledEvidenceFiles, error) {
	_, artifacts := planIndex(plan)
	rootArtifact := artifacts[expected.ArtifactID]
	if root == nil || rootArtifact.Component == "" {
		return reconciledEvidenceFiles{}, e.New(e.CodeProvenanceMissing, "reconciled evidence lacks its raw root")
	}
	if documents == nil {
		documents = make(map[string]*sbom.BOM)
	}
	if previous := documents[rootArtifact.Component]; previous != nil {
		left, err := previous.Digest()
		if err != nil {
			return reconciledEvidenceFiles{}, err
		}
		right, err := root.Digest()
		if err != nil || left != right {
			return reconciledEvidenceFiles{}, e.New(e.CodeVerifyFailed, "root inventory conflicts with component evidence")
		}
	}
	documents[rootArtifact.Component] = root
	if len(documents) != len(expected.Components) {
		return reconciledEvidenceFiles{}, e.New(e.CodeProvenanceMissing, "disk evidence does not cover every exact component slot")
	}
	set := artifacts["sbom/"+expected.Name]
	if set.Kind != "sbom-set" || set.BuildTarget != expected.Name || set.Path == "" {
		return reconciledEvidenceFiles{}, e.New(e.CodePipelineDrift, "disk output has no compiler-issued evidence set")
	}
	result := reconciledEvidenceFiles{directory: set.Path, files: make(map[string][]byte, len(documents)+1)}
	index := &sbom.Index{SchemaVersion: sbom.IndexSchemaVersion, BuildDigest: expected.BuildDigest}
	for _, component := range expected.Components {
		bom := documents[component]
		record := artifacts["result/"+expected.Name+"/"+component]
		if record.Kind != "producer-result" || record.Component != component || record.BuildTarget != expected.Name {
			return reconciledEvidenceFiles{}, e.New(e.CodeProvenanceMissing, "disk inventory lacks its exact component producer record")
		}
		observed, err := readProducerResult(ctx, record.Path)
		if err != nil {
			return reconciledEvidenceFiles{}, err
		}
		if observed.Component != component || !canonical.IsDigest(observed.SHA256) {
			return reconciledEvidenceFiles{}, e.New(e.CodeVerifyFailed, "disk inventory producer record differs from its planned component")
		}
		if err := validateReconciledDiskDocument(plan, expected.Name, component, observed.SHA256, bom); err != nil {
			return reconciledEvidenceFiles{}, err
		}
		raw, err := bom.Marshal()
		if err != nil {
			return reconciledEvidenceFiles{}, err
		}
		if len(raw) >= sbom.MaxDocumentBytes {
			return reconciledEvidenceFiles{}, e.New(e.CodeVerifyFailed, "complete disk SBOM exceeds its newline-delimited file bound")
		}
		digest, err := bom.Digest()
		if err != nil {
			return reconciledEvidenceFiles{}, err
		}
		result.files[component+".cdx.json"] = append(raw, '\n')
		index.Entries = append(index.Entries, sbom.IndexEntry{Artifact: component, SBOMDigest: digest, ArtifactSHA: observed.SHA256})
	}
	if err := index.Validate(); err != nil {
		return reconciledEvidenceFiles{}, err
	}
	raw, err := canonical.Marshal(index)
	if err != nil || len(raw) >= sbom.MaxDocumentBytes {
		return reconciledEvidenceFiles{}, e.New(e.CodeVerifyFailed, "disk evidence index exceeds its bounded canonical policy")
	}
	result.files["index.json"] = append(raw, '\n')
	return result, nil
}

// writeTo is used only beneath an invocation-private package stage. Final
// evidence membership is published with its disk wrapper, never file by file.
func (files reconciledEvidenceFiles) writeTo(ctx context.Context, directory string) error {
	for _, name := range sortedKeys(files.files) {
		if name == "." || name == ".." || filepath.Base(name) != name {
			return e.New(e.CodePipelineDrift, "disk evidence has an unsafe staged member")
		}
		if err := writeFactoryFile(ctx, filepath.Join(directory, name), files.files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}
