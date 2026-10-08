// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package attest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
)

type acquisitionFixture struct {
	component string
	contents  []byte
}

type attestSourceExecutionObserver struct {
	observation profile.ExecutionObservation
}

type attestDocumentVerifier struct{}

func (attestDocumentVerifier) VerifyDocument(context.Context,
	trust.DocumentVerificationRequest,
) (trust.VerificationEvidence, error) {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{"publisher@example.test"},
		OIDCIssuer:            "https://issuer.example.test",
		CertificateSHA256:     attestDigest('c'),
	}, nil
}

func (observer attestSourceExecutionObserver) ObserveExecution(context.Context,
	pipeline.ExecutionObservationRequest,
) (profile.ExecutionObservation, error) {
	return observer.observation, nil
}

func qualifiedAcquisitionReport(t *testing.T, fixtures []acquisitionFixture) *pipeline.Report {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	options := pipeline.RunOptions{Ctx: context.Background(), SourceRoot: filepath.Join(work, "src")}
	graph := &ir.Graph{SchemaVersion: ir.SchemaVersion, Platform: "linux/amd64"}
	verify := ir.Node{ID: "verify", Kind: ir.KindVerify}
	for _, fixture := range fixtures {
		sum := sha256.Sum256(fixture.contents)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		node := ir.Node{ID: "pull-" + fixture.component, Kind: ir.KindPull,
			Component: fixture.component, Artifact: "https://example.test/" + fixture.component,
			Digest: digest, Platform: "linux/amd64", Network: ir.NetworkDefault,
			OutputType: "raw", ArtifactPath: fixture.component, Version: "1.0.0"}
		graph.Nodes = append(graph.Nodes, node)
		verify.Needs = append(verify.Needs, node.ID)
		artifactPath := filepath.Join(filepath.Dir(options.SourceRoot), "fetch", fixture.component, fixture.component)
		if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(artifactPath, fixture.contents, 0o644); err != nil {
			t.Fatal(err)
		}
		result := pipeline.ArtifactResult{Component: fixture.component, OutputType: "raw",
			Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire,
			Path: fixture.component, SHA256: digest, Size: int64(len(fixture.contents))}
		writePipelineResult(t, filepath.Join(filepath.Dir(options.SourceRoot), "results"), node.ID, result)
	}
	graph.Nodes = append(graph.Nodes, verify)
	graph.Normalize()
	buildDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	report, err := (pipeline.Local{}).VerifyResults(graph, buildDigest,
		filepath.Join(filepath.Dir(options.SourceRoot), "results"), options)
	if err != nil {
		t.Fatalf("terminal acquisition verification failed: %v", err)
	}
	t.Cleanup(func() { _ = report.Close() })
	return report
}

func writePipelineResult(t *testing.T, dir, nodeID string, result pipeline.ArtifactResult) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nodeID+".json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func qualifiedSourceEvidenceFixture(t *testing.T, repo, toolchain string) (*buildgraph.Resolved,
	*ir.Graph, *pipeline.Report,
) {
	t.Helper()
	resolved := &buildgraph.Resolved{
		Platform: "linux/amd64",
		Components: []catalog.NamedComponent{{
			Name: "agent",
			Component: catalog.Component{Platforms: []string{"linux/amd64"},
				FromSource: true, Repo: repo, Tag: "v1.0.0",
				Commit: strings.Repeat("d", 40), TreeHash: strings.Repeat("e", 40),
				ToolchainImage: toolchain, OutputType: "raw",
				BuildCommand: []string{"true"}, ArtifactPath: "out/agent", WriteDir: "out",
				SourceDate: 1,
			},
		}},
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	buildDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var build ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindBuild {
			build = node
		}
	}
	if build.ID == "" {
		t.Fatal("source fixture compiled no build node")
	}

	runnerDigest := attestDigest('a')
	runtimeDigest := attestDigest('b')
	toolchainDigest := toolchain[strings.LastIndex(toolchain, "@")+1:]
	builderProfile := profile.BuilderProfile{
		SchemaVersion: profile.SchemaVersion, Identity: "publisher-source-fixture", Version: "1",
		HostControlClass: profile.OperatorControlled, Provides: []string{ir.BuilderOCIHermetic},
		Fields: []profile.Field{
			{Name: profile.FieldTargetArch, Value: "amd64", HardFail: true, MeasurementStage: profile.MeasurementHost},
			{Name: profile.FieldRunnerDigest, Value: runnerDigest, HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldRuntimeIdentity, Value: runtimeDigest, HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldOCISandboxMode, Value: "fixture-sandbox", HardFail: true, MeasurementStage: profile.MeasurementExecution},
		},
	}
	host, divergences, err := builderProfile.VerifyHost(context.Background(),
		map[string]string{profile.FieldTargetArch: "amd64"})
	if err != nil || len(divergences) != 0 {
		t.Fatalf("source fixture host preflight: %v %+v", err, divergences)
	}
	observation := profile.ExecutionObservation{
		RunnerDigest: runnerDigest, RunnerOrigin: profile.RunnerPreexisting,
		RuntimeIdentity: runtimeDigest,
		Nodes: []profile.NodeExecutionObservation{{
			NodeID: build.ID, EngineClass: ir.EngineOCIHermetic,
			ToolchainDigest: toolchainDigest, SandboxMode: "fixture-sandbox",
		}},
	}
	execution, err := profile.VerifyExecution(context.Background(), host, builderProfile,
		[]ir.Node{build}, observation)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	options := pipeline.RunOptions{
		Ctx: context.Background(), SourceRoot: filepath.Join(work, "src"), Runtime: os.Args[0],
		Qualification: &pipeline.QualifiedExecution{
			Profile: builderProfile, HostReceipt: host,
			Observer: attestSourceExecutionObserver{observation: observation},
		},
	}
	artifactPath := pipeline.ArtifactPath(options, build)
	payload := []byte("mirror-invariant-built-artifact")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	result := pipeline.ArtifactResult{
		Component: build.Component, OutputType: build.OutputType, Toolchain: build.Toolchain,
		Qualification: pipeline.ArtifactQualifiedExecution,
		InputDigest:   attestDigest('f'), BuilderType: build.BuilderType,
		EngineClass: ir.EngineOCIHermetic, ProfileDigest: execution.ProfileDigest(),
		ExecutionBinding: execution.ExecutionBinding(), Path: build.ArtifactPath,
		SHA256: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(payload)),
	}
	resultsDir := filepath.Join(filepath.Dir(options.SourceRoot), "results")
	writePipelineResult(t, resultsDir, build.ID, result)
	report, err := (pipeline.Local{}).VerifyResults(graph, buildDigest, resultsDir, options)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, graph, report
}

func attestDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func TestPublisherEvidenceComparisonRequiresAuthenticatedExactVector(t *testing.T) {
	row := publisherEvidenceRow{
		Component: "agent", ArtifactDigest: attestDigest('a'),
		SBOMDigest: attestDigest('b'), SBOMBlobDigest: attestDigest('c'),
		SBOMEvidenceSchema: "cyclonedx-json/1.5",
		ProvenanceDigest:   attestDigest('d'), ProvenanceBlobDigest: attestDigest('e'),
		ProvenanceSchema: "in-toto-slsa-provenance/v1",
	}
	core := publisherEvidenceCore{
		SchemaVersion: PublisherEvidenceSchemaVersion, BuildDigest: attestDigest('1'),
		SBOMIndexDigest: attestDigest('2'), ProvenanceIndexDigest: attestDigest('3'),
		Artifacts: []publisherEvidenceRow{row},
	}
	digest, err := canonical.Digest(canonical.KindPublisherEvidence, core)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &PublisherEvidence{valid: true, core: core, digest: digest,
		sbomReceipts:       make([]sbom.LocalEvidenceReceipt, 1),
		provenanceReceipts: make([]provenance.LocalEvidenceReceipt, 1)}
	manifest := &Manifest{
		SchemaVersion: SchemaVersion, BuildDigest: core.BuildDigest,
		Evidence: &EvidenceRef{SBOMDigest: core.SBOMIndexDigest,
			ProvenanceDigest: core.ProvenanceIndexDigest},
		EvidenceDigest: digest,
		Components: []ComponentDigest{{Name: row.Component, OutputType: "raw",
			EngineClass: ir.EnginePrebuiltAcquire, SHA256: row.ArtifactDigest}},
	}
	receipt := publishedReceiptFixture(t, manifest)
	if err := evidence.VerifyAgainstPublishedVector(context.Background(), receipt); err != nil {
		t.Fatalf("exact authenticated vector rejected: %v", err)
	}
	binding, err := evidence.Binding()
	if err != nil {
		t.Fatal(err)
	}
	binding.Artifacts[0].SBOMEvidenceSchema = "cyclonedx-json/1.6"
	wrongSchemaCore := core
	wrongSchemaCore.Artifacts = append([]publisherEvidenceRow(nil), core.Artifacts...)
	wrongSchemaCore.Artifacts[0].SBOMEvidenceSchema = binding.Artifacts[0].SBOMEvidenceSchema
	binding.EvidenceDigest, err = canonical.Digest(canonical.KindPublisherEvidence, wrongSchemaCore)
	if err != nil {
		t.Fatal(err)
	}
	if attestErrorCode(binding.Validate()) != nkderr.CodeUnverifiedRef {
		t.Fatal("unsupported SBOM evidence schema remained valid after exact aggregate rehash")
	}
	expanded := *manifest
	expanded.Components = append(append([]ComponentDigest(nil), manifest.Components...),
		ComponentDigest{Name: "zeta", OutputType: "raw", EngineClass: ir.EnginePrebuiltAcquire,
			SHA256: attestDigest('f')})
	if err := evidence.VerifyAgainstPublishedVector(context.Background(), publishedReceiptFixture(t, &expanded)); attestErrorCode(err) != nkderr.CodeVerifyFailed || !strings.Contains(err.Error(), "selected subset") {
		t.Fatalf("subset evidence claimed the full authenticated vector aggregate: %v", err)
	}

	wrong := *manifest
	wrong.EvidenceDigest = attestDigest('9')
	if err := evidence.VerifyAgainstPublishedVector(context.Background(), publishedReceiptFixture(t, &wrong)); attestErrorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("aggregate substitution did not fail closed: %v", err)
	}
	if err := evidence.VerifyAgainstPublishedVector(context.Background(), PublishedVectorReceipt{}); attestErrorCode(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("raw/zero vector substituted for authenticated receipt: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := evidence.VerifyAgainstPublishedVector(ctx, receipt); attestErrorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled comparison returned %v", err)
	}
}

func TestValidationVectorRejectsRemovedDeploymentEvidenceFields(t *testing.T) {
	for _, field := range []string{"e2eTestDigest", "clusterValidationDigest"} {
		t.Run(field, func(t *testing.T) {
			manifest := man('a', prebuiltComponent("agent", "raw", '1'))
			raw, err := manifest.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			// Unknown deployment evidence must fail strict decoding even when its
			// value is a valid digest and the surrounding document is current.
			raw = bytes.Replace(raw, []byte(`"evidence":{`), []byte(`"evidence":{"`+field+`":"`+attestDigest('4')+`",`), 1)
			var decoded Manifest
			if err := canonical.UnmarshalExact(raw, &decoded); attestErrorCode(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("removed deployment evidence decoded: %v", err)
			}
		})
	}
}

func TestAuthenticatedPublishedVectorRejectsLegacyOperatorEvidenceFields(t *testing.T) {
	for name, mutate := range map[string]func(*EvidenceRef){
		"vulnerability": func(ref *EvidenceRef) {
			ref.VulnReportDigest = attestDigest('6')
		},
	} {
		t.Run(name, func(t *testing.T) {
			manifest := man('a', prebuiltComponent("agent", "raw", '1'))
			mutate(manifest.Evidence)
			raw, err := manifest.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "vector.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			authenticator, err := trust.NewDocumentAuthenticator(attestDocumentVerifier{},
				trust.JSONSchema[Manifest](SchemaVersion))
			if err != nil {
				t.Fatal(err)
			}
			policy, err := trust.NewRolePolicy(trust.RoleValidationVector,
				trust.Store{OperatorIdentities: []string{"publisher@example.test"}},
				"https://issuer.example.test")
			if err != nil {
				t.Fatal(err)
			}
			document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
				SourcePath: path, Role: trust.RoleValidationVector, Policy: policy,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyPublishedVectorReceipt(context.Background(), document); attestErrorCode(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("authenticated vector retained legacy operator evidence authority: %v", err)
			}
		})
	}
}

func TestPublisherEvidenceIsMirrorInvariant(t *testing.T) {
	toolchainDigest := attestDigest('c')
	onlineResolved, onlineGraph, onlineReport := qualifiedSourceEvidenceFixture(t,
		"https://source.online.example/agent.git",
		"registry.online.example/toolchain@"+toolchainDigest)
	mirrorResolved, mirrorGraph, mirrorReport := qualifiedSourceEvidenceFixture(t,
		"https://source.airgap.example/mirror/agent.git",
		"registry.airgap.example/mirror/toolchain@"+toolchainDigest)
	if onlineReport.BuildDigest != mirrorReport.BuildDigest {
		t.Fatalf("transport relocation changed build identity: %s != %s",
			onlineReport.BuildDigest, mirrorReport.BuildDigest)
	}

	buildEvidence := func(resolved *buildgraph.Resolved, graph *ir.Graph,
		report *pipeline.Report,
	) *PublisherEvidence {
		verifiedSBOMs, err := sbom.BuildVerifiedFromReport(context.Background(), resolved, report, nil)
		if err != nil {
			t.Fatal(err)
		}
		qualifiedProvenance, err := provenance.BuildQualifiedFromReport(context.Background(), report, graph)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := QualifyPublisherEvidence(context.Background(), verifiedSBOMs, qualifiedProvenance)
		if err != nil {
			t.Fatal(err)
		}
		return evidence
	}
	online := buildEvidence(onlineResolved, onlineGraph, onlineReport)
	mirror := buildEvidence(mirrorResolved, mirrorGraph, mirrorReport)
	onlineBinding, err := online.Binding()
	if err != nil {
		t.Fatal(err)
	}
	mirrorBinding, err := mirror.Binding()
	if err != nil {
		t.Fatal(err)
	}
	if onlineBinding.BuildDigest != mirrorBinding.BuildDigest ||
		onlineBinding.SBOMIndexDigest != mirrorBinding.SBOMIndexDigest ||
		onlineBinding.ProvenanceIndexDigest != mirrorBinding.ProvenanceIndexDigest ||
		onlineBinding.EvidenceDigest != mirrorBinding.EvidenceDigest ||
		len(onlineBinding.Artifacts) != 1 || len(mirrorBinding.Artifacts) != 1 ||
		onlineBinding.Artifacts[0] != mirrorBinding.Artifacts[0] {
		t.Fatalf("online/mirror transports changed publisher evidence:\nonline=%+v\nmirror=%+v",
			onlineBinding, mirrorBinding)
	}
}

func publishedReceiptFixture(t *testing.T, manifest *Manifest) PublishedVectorReceipt {
	t.Helper()
	payload, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := manifest.Identity()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	return PublishedVectorReceipt{valid: true, identity: identity,
		blobDigest: "sha256:" + hex.EncodeToString(sum[:]), signer: "publisher@example.test",
		canonicalPayload: string(payload)}
}

func attestErrorCode(err error) nkderr.Code {
	if coded, ok := err.(*nkderr.Error); ok {
		return coded.Code
	}
	return ""
}

func prebuiltComponent(name, outputType string, digest byte) ComponentDigest {
	return ComponentDigest{
		Name: name, OutputType: outputType, EngineClass: ir.EnginePrebuiltAcquire,
		SHA256: attestDigest(digest),
	}
}

func sourceComponent(name, outputType string, artifact, input, toolchain byte) ComponentDigest {
	return ComponentDigest{
		Name: name, OutputType: outputType,
		Toolchain:   attestDigest(toolchain),
		InputDigest: attestDigest(input), BuilderType: ir.BuilderOCIHermetic,
		EngineClass: ir.EngineOCIHermetic, SHA256: attestDigest(artifact),
	}
}

func TestValidationVectorProjectionIsInvariantToToolchainTransport(t *testing.T) {
	base := pipeline.Report{
		BuildDigest: attestDigest('a'),
		Artifacts: []pipeline.ArtifactResult{{
			Component: "kubelet", OutputType: "host-package",
			Toolchain:   "registry.example/toolchain@" + attestDigest('2'),
			InputDigest: attestDigest('3'), BuilderType: ir.BuilderOCIHermetic,
			EngineClass: ir.EngineOCIHermetic, SHA256: attestDigest('4'),
		}},
	}
	mirrored := base
	mirrored.Artifacts = append([]pipeline.ArtifactResult(nil), base.Artifacts...)
	mirrored.Artifacts[0].Toolchain = "airgap.local/mirror/toolchain@" + attestDigest('2')

	online := FromReport(&base)
	offline := FromReport(&mirrored)
	for _, manifest := range []*Manifest{online, offline} {
		manifest.SetBuilderProfile("", attestDigest('5'), "")
		manifest.Evidence = &EvidenceRef{
			SBOMDigest: attestDigest('6'), ProvenanceDigest: attestDigest('7'),
		}
		manifest.EvidenceDigest = attestDigest('8')
	}
	if online.Components[0].Toolchain != attestDigest('2') ||
		offline.Components[0].Toolchain != attestDigest('2') {
		t.Fatalf("toolchain transport leaked into vector projection: online=%q offline=%q",
			online.Components[0].Toolchain, offline.Components[0].Toolchain)
	}
	onlineBytes, err := online.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	offlineBytes, err := offline.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(onlineBytes) != string(offlineBytes) {
		t.Fatalf("toolchain mirror relocation changed validation-vector bytes:\n online %s\noffline %s",
			onlineBytes, offlineBytes)
	}
}

func man(build byte, comps ...ComponentDigest) *Manifest {
	m := &Manifest{
		SchemaVersion: SchemaVersion, BuildDigest: attestDigest(build),
		Evidence:       &EvidenceRef{SBOMDigest: attestDigest('6'), ProvenanceDigest: attestDigest('7')},
		EvidenceDigest: attestDigest('8'),
		Components:     append([]ComponentDigest(nil), comps...),
	}
	sort.Slice(m.Components, func(i, j int) bool { return m.Components[i].Name < m.Components[j].Name })
	for _, component := range m.Components {
		if component.BuilderType != "" {
			m.SetBuilderProfile("", attestDigest('9'), "")
			break
		}
	}
	return m
}

func TestVerifyMatch(t *testing.T) {
	want := man('a', prebuiltComponent("kubectl", "host-package", '1'))
	got := man('a', prebuiltComponent("kubectl", "host-package", '1'))
	if err := want.VerifyAgainst(got); err != nil {
		t.Fatalf("identical builds must compare equal: %v", err)
	}
}

func TestVerifyFailsClosed(t *testing.T) {
	base := man('a', prebuiltComponent("kubectl", "host-package", '1'))
	cases := map[string]*Manifest{
		"buildDigest mismatch": man('b', prebuiltComponent("kubectl", "host-package", '1')),
		"component diverged":   man('a', prebuiltComponent("kubectl", "host-package", '2')),
		"component missing":    {SchemaVersion: SchemaVersion, BuildDigest: attestDigest('a')},
	}
	for name, got := range cases {
		if code(base.VerifyAgainst(got)) != nkderr.CodeVerifyFailed {
			t.Errorf("%s: expected CodeVerifyFailed, got %v", name, base.VerifyAgainst(got))
		}
	}
}

func TestVerifyEngineClassMismatchFailsClosed(t *testing.T) {
	want := man('a', sourceComponent("kubelet", "host-package", '1', '2', '3'))
	got := man('a', sourceComponent("kubelet", "host-package", '1', '2', '3'))
	got.Components[0].EngineClass = ir.EngineNix
	if code(want.VerifyAgainst(got)) != nkderr.CodeVerifyFailed {
		t.Fatal("substrate mismatch must fail closed")
	}
	if err := want.VerifyAgainst(man('a', sourceComponent("kubelet", "host-package", '1', '2', '3'))); err != nil {
		t.Fatalf("matching substrate must compare equal: %v", err)
	}
}

func TestVerifyResolvedInputIdentityIsMandatoryAndExact(t *testing.T) {
	want := man('a', sourceComponent("kubelet", "host-package", '1', '2', '3'))
	for name, mutate := range map[string]func(*Manifest){
		"missing":   func(got *Manifest) { got.Components[0].InputDigest = "" },
		"different": func(got *Manifest) { got.Components[0].InputDigest = attestDigest('4') },
	} {
		got := man('a', sourceComponent("kubelet", "host-package", '1', '2', '3'))
		mutate(got)
		if code(want.VerifyAgainst(got)) != nkderr.CodeVerifyFailed {
			t.Fatalf("%s resolved-input identity must fail reproduction", name)
		}
	}
}

func TestVerifySubsetIsScopedToBuiltComponents(t *testing.T) {
	full := man('a',
		prebuiltComponent("etcd", "host-package", '3'),
		prebuiltComponent("kubeadm", "host-package", '2'),
		prebuiltComponent("kubectl", "host-package", '1'))

	if err := full.VerifyAgainst(man('a', prebuiltComponent("kubectl", "host-package", '1'))); err != nil {
		t.Fatalf("matching subset must compare equal for that subset: %v", err)
	}
	if code(full.VerifyAgainst(man('a', prebuiltComponent("kubectl", "host-package", '9')))) != nkderr.CodeVerifyFailed {
		t.Fatal("tampered subset must fail closed")
	}
	if code(full.VerifyAgainst(man('a', prebuiltComponent("mystery", "host-package", '1')))) != nkderr.CodeVerifyFailed {
		t.Fatal("unattested built component must fail closed")
	}
	evidence := full.BuildEvidence(man('a', prebuiltComponent("kubectl", "host-package", '1')), VerdictReproduced, nil)
	if len(evidence.Components) != 1 || evidence.Components[0].Name != "kubectl" {
		t.Fatalf("subset evidence must not claim an unbuilt member: %+v", evidence.Components)
	}
}

func code(err error) nkderr.Code {
	if coded, ok := err.(*nkderr.Error); ok {
		return coded.Code
	}
	return ""
}

func TestVerifyProfileBounded(t *testing.T) {
	want := man('a', sourceComponent("kubectl", "host-package", '1', '2', '3'))
	want.SetBuilderProfile("", attestDigest('4'), "")
	got := man('a', sourceComponent("kubectl", "host-package", '1', '2', '3'))
	got.SetBuilderProfile("", attestDigest('5'), "")
	if code(want.VerifyAgainst(got)) != nkderr.CodeVerifyFailed {
		t.Fatal("a different builder profile must fail the Reproduced comparison")
	}
	got.SetBuilderProfile("", attestDigest('4'), "")
	if err := want.VerifyAgainst(got); err != nil {
		t.Fatalf("same profile must compare equal: %v", err)
	}
}

func TestFromReportIsSortedButExplicitlyUnqualified(t *testing.T) {
	rep := &pipeline.Report{
		BuildDigest: attestDigest('a'),
		Artifacts: []pipeline.ArtifactResult{
			{Component: "kubectl", OutputType: "host-package", Toolchain: "registry.example/toolchain@" + attestDigest('2'), InputDigest: attestDigest('4'), BuilderType: ir.BuilderOCIHermetic, EngineClass: ir.EngineOCIHermetic, SHA256: attestDigest('6')},
			{Component: "etcd", OutputType: "oci-image", Toolchain: "registry.example/toolchain@" + attestDigest('1'), InputDigest: attestDigest('3'), BuilderType: ir.BuilderOCIHermetic, EngineClass: ir.EngineOCIHermetic, SHA256: attestDigest('5')},
		},
	}
	m := FromReport(rep)
	if m.Components[0].Name != "etcd" || m.Components[1].InputDigest != attestDigest('4') {
		t.Fatalf("pure report projection lost deterministic ordering or input identity: %+v", m.Components)
	}
	if _, err := m.Marshal(); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("unqualified from-source projection must not become an authority document: %v", err)
	}
	if code(man('a', sourceComponent("etcd", "oci-image", '5', '3', '1')).VerifyQualifiedAgainst(context.Background(), m)) != nkderr.CodePreflightFailed {
		t.Fatal("pure FromReport projection must not satisfy qualified comparison")
	}
}

func TestManifestValidateRejectsAmbiguousAuthorityData(t *testing.T) {
	valid := man('a', sourceComponent("kubelet", "host-package", '1', '2', '3'))
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid from-source vector rejected: %v", err)
	}

	tests := map[string]func(*Manifest){
		"schema":              func(m *Manifest) { m.SchemaVersion = "nkd.dev/validation-vector/v1" },
		"build digest":        func(m *Manifest) { m.BuildDigest = "sha256:ABCD" },
		"profile mismatch":    func(m *Manifest) { m.BuilderProfile.Digest = attestDigest('8') },
		"component digest":    func(m *Manifest) { m.Components[0].SHA256 = "sha256:1" },
		"evidence digest":     func(m *Manifest) { m.Evidence = &EvidenceRef{SBOMDigest: "sha256:short"} },
		"aggregate evidence":  func(m *Manifest) { m.EvidenceDigest = "SHA256:" + strings.Repeat("a", 64) },
		"input digest":        func(m *Manifest) { m.Components[0].InputDigest = "sha256:2" },
		"builder erasure":     func(m *Manifest) { m.Components[0].BuilderType = "" },
		"unknown builder":     func(m *Manifest) { m.Components[0].BuilderType = "native"; m.Components[0].EngineClass = "native" },
		"engine disagreement": func(m *Manifest) { m.Components[0].EngineClass = ir.EngineNix },
		"unpinned toolchain":  func(m *Manifest) { m.Components[0].Toolchain = "registry.example/toolchain:latest" },
	}
	for name, mutate := range tests {
		candidate := man('a', sourceComponent("kubelet", "host-package", '1', '2', '3'))
		mutate(candidate)
		if code(candidate.Validate()) != nkderr.CodeVerifyFailed {
			t.Errorf("%s mutation passed semantic validation", name)
		}
	}

	allPrebuiltWithProfile := man('a', prebuiltComponent("kubectl", "host-package", '1'))
	allPrebuiltWithProfile.SetBuilderProfile("", attestDigest('9'), "")
	if code(allPrebuiltWithProfile.Validate()) != nkderr.CodeVerifyFailed {
		t.Fatal("all-prebuilt vector accepted a builder profile")
	}
	fromSourceWithoutProfile := man('a', sourceComponent("kubectl", "host-package", '1', '2', '3'))
	fromSourceWithoutProfile.ProfileDigest, fromSourceWithoutProfile.BuilderProfile = "", nil
	if code(fromSourceWithoutProfile.Validate()) != nkderr.CodeVerifyFailed {
		t.Fatal("from-source vector accepted a missing builder profile")
	}
}

func TestManifestValidateRejectsDuplicateAndUnsortedRows(t *testing.T) {
	duplicate := man('a',
		prebuiltComponent("kubectl", "host-package", '1'),
		prebuiltComponent("kubectl", "host-package", '2'))
	if code(duplicate.Validate()) != nkderr.CodeVerifyFailed {
		t.Fatal("duplicate component names must fail closed")
	}

	unsorted := man('a',
		prebuiltComponent("etcd", "host-package", '1'),
		prebuiltComponent("kubectl", "host-package", '2'))
	unsorted.Components[0], unsorted.Components[1] = unsorted.Components[1], unsorted.Components[0]
	if code(unsorted.Validate()) != nkderr.CodeVerifyFailed {
		t.Fatal("unsorted component rows must fail closed")
	}

}

func TestParseRequiresExactCanonicalBytesAndSemanticValidity(t *testing.T) {
	valid := man('a', prebuiltComponent("kubectl", "host-package", '1'))
	raw, err := valid.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err != nil {
		t.Fatalf("canonical bytes rejected: %v", err)
	}
	if _, err := Parse(append(append([]byte(nil), raw...), '\n')); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("noncanonical trailing byte accepted: %v", err)
	}

	invalid := man('a', prebuiltComponent("kubectl", "host-package", '1'))
	invalid.Components[0].SHA256 = "sha256:short"
	semanticBytes, err := canonical.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(semanticBytes); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("canonical but semantically invalid document accepted: %v", err)
	}
}

func TestComparisonRejectsCriticalIdentityDrift(t *testing.T) {
	newPair := func() (*Manifest, *Manifest) {
		components := []ComponentDigest{
			sourceComponent("kubelet", "host-package", '1', '3', '5'),
			sourceComponent("scheduler", "host-package", '2', '4', '6'),
		}
		return man('a', components...), man('a', components...)
	}
	tests := map[string]func(*Manifest){
		"output type": func(got *Manifest) { got.Components[0].OutputType = "oci-image" },
		"toolchain":   func(got *Manifest) { got.Components[0].Toolchain = "registry.example/toolchain@" + attestDigest('7') },
		"builder": func(got *Manifest) {
			got.Components[0].BuilderType = ir.BuilderNix
			got.Components[0].EngineClass = ir.EngineNix
		},
		"engine": func(got *Manifest) { got.Components[0].EngineClass = ir.EngineNix },
		"input":  func(got *Manifest) { got.Components[0].InputDigest = attestDigest('7') },
		"builder erasure": func(got *Manifest) {
			got.Components[0].BuilderType, got.Components[0].EngineClass = "", ir.EnginePrebuiltAcquire
			got.Components[0].Toolchain, got.Components[0].InputDigest = "", ""
		},
	}
	for name, mutate := range tests {
		want, got := newPair()
		mutate(got)
		if code(want.VerifyAgainst(got)) != nkderr.CodeVerifyFailed {
			t.Errorf("VerifyAgainst accepted %s drift", name)
		}
	}
}

func TestFromQualifiedReportRejectsUnqualifiedReport(t *testing.T) {
	rep := &pipeline.Report{BuildDigest: attestDigest('a'), Artifacts: []pipeline.ArtifactResult{{
		Component: "kubectl", OutputType: "host-package", Qualification: pipeline.ArtifactUnqualified,
		EngineClass: ir.EnginePrebuiltAcquire, SHA256: attestDigest('1'),
	}}}
	if _, err := FromQualifiedReport(context.Background(), rep); code(err) != nkderr.CodePreflightFailed {
		t.Fatalf("unqualified report became a validation vector: %v", err)
	}
}

func TestFromQualifiedReportAcceptsProfileFreeAcquisition(t *testing.T) {
	ctx := context.Background()
	report := qualifiedAcquisitionReport(t, []acquisitionFixture{{component: "kubectl", contents: []byte("kubectl")}})
	manifest, err := FromQualifiedReport(ctx, report)
	if err != nil {
		t.Fatalf("qualified acquisition projection failed: %v", err)
	}
	if manifest.ProfileDigest != "" || manifest.BuilderProfile != nil {
		t.Fatalf("byte-preserving acquisition unexpectedly gained a profile: %+v", manifest.BuilderProfile)
	}
	manifest.Evidence = &EvidenceRef{SBOMDigest: attestDigest('6'), ProvenanceDigest: attestDigest('7')}
	manifest.EvidenceDigest = attestDigest('8')
	if err := manifest.VerifyQualifiedAgainst(ctx, manifest); err != nil {
		t.Fatalf("qualified profile-free observation rejected: %v", err)
	}
	if _, err := manifest.MarshalQualified(ctx); code(err) != nkderr.CodeProvenanceMissing {
		t.Fatalf("caller-authored evidence acquired publisher authority: %v", err)
	}
}

func TestPublishedManifestRejectsLegacyCallerAuthoredConformance(t *testing.T) {
	report := qualifiedAcquisitionReport(t, []acquisitionFixture{{component: "kubectl", contents: []byte("kubectl")}})
	manifest, err := FromQualifiedReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Evidence = &EvidenceRef{SBOMDigest: attestDigest('6'), ProvenanceDigest: attestDigest('7')}
	manifest.EvidenceDigest = attestDigest('8')

	for name, mutate := range map[string]func(*Manifest){
		"conformance claim":    func(candidate *Manifest) { candidate.ConformanceClaim = "conformant" },
		"validation reference": func(candidate *Manifest) { candidate.ValidationVectorRef = attestDigest('9') },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *manifest
			mutate(&candidate)
			if err := candidate.ValidatePublished(); code(err) != nkderr.CodeProvenanceMissing {
				t.Fatalf("caller-authored later-gate metadata became publishable: %v", err)
			}
		})
	}
}

func TestQualifiedManifestCloseReleasesReportAuthority(t *testing.T) {
	ctx := context.Background()
	report := qualifiedAcquisitionReport(t, []acquisitionFixture{{component: "kubectl", contents: []byte("kubectl")}})
	manifest, err := FromQualifiedReport(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Close(); err != nil {
		t.Fatalf("closing qualified manifest: %v", err)
	}
	if err := manifest.VerifyQualifiedAgainst(ctx, manifest); code(err) != nkderr.CodePreflightFailed {
		t.Fatalf("closed manifest retained report authority: %v", err)
	}
	if err := manifest.Close(); err != nil {
		t.Fatalf("qualified manifest close was not idempotent: %v", err)
	}
}

func TestQualifiedObservationRejectsManifestAndReportMutation(t *testing.T) {
	want, got, _ := qualifiedSourceVector(t)
	if err := want.VerifyQualifiedAgainst(context.Background(), got); err != nil {
		t.Fatalf("fresh qualified observation must verify: %v", err)
	}
	if got.ProfileDigest != "" || got.BuilderProfile != nil {
		t.Fatalf("profile-free qualified vector unexpectedly gained a builder profile: %+v", got.BuilderProfile)
	}
	got.ConformanceClaim = "conformant"
	got.Evidence = &EvidenceRef{SBOMDigest: attestDigest('a'), ProvenanceDigest: attestDigest('c')}
	got.EvidenceDigest = attestDigest('b')
	if err := want.VerifyQualifiedAgainst(context.Background(), got); err != nil {
		t.Fatalf("validated metadata enrichment must preserve report-core qualification: %v", err)
	}

	got.Components[0].SHA256 = attestDigest('e')
	if code(want.VerifyQualifiedAgainst(context.Background(), got)) != nkderr.CodeVerifyFailed {
		t.Fatal("post-qualification manifest mutation was accepted")
	}
	if _, err := got.MarshalQualified(context.Background()); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("mutated report core was published as qualified bytes: %v", err)
	}

	var report *pipeline.Report
	want, got, report = qualifiedSourceVector(t)
	report.Artifacts[0].SHA256 = attestDigest('e')
	if code(want.VerifyQualifiedAgainst(context.Background(), got)) != nkderr.CodeVerifyFailed {
		t.Fatal("post-qualification source-report mutation was accepted")
	}

	want, got, report = qualifiedSourceVector(t)
	unqualified := FromReport(report)
	unqualified.SetBuilderProfile("", got.ProfileDigest, "")
	if code(want.VerifyQualifiedAgainst(context.Background(), unqualified)) != nkderr.CodePreflightFailed {
		t.Fatal("caller-composed manifest was accepted as a qualified observation")
	}
}

func qualifiedSourceVector(t *testing.T) (want, got *Manifest, report *pipeline.Report) {
	t.Helper()
	ctx := context.Background()
	report = qualifiedAcquisitionReport(t, []acquisitionFixture{{component: "kubelet", contents: []byte("kubelet")}})
	var err error
	got, err = FromQualifiedReport(ctx, report)
	if err != nil {
		t.Fatalf("qualified report projection failed: %v", err)
	}
	got.Evidence = &EvidenceRef{SBOMDigest: attestDigest('6'), ProvenanceDigest: attestDigest('7')}
	got.EvidenceDigest = attestDigest('8')
	raw, err := got.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want, err = Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return want, got, report
}
