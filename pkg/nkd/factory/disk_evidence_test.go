package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

type diskEvidenceExportMutationWriter struct {
	bytes.Buffer
	mutate     func()
	afterBytes int
}

func (writer *diskEvidenceExportMutationWriter) Write(raw []byte) (int, error) {
	written, err := writer.Buffer.Write(raw)
	if writer.mutate != nil && writer.Len() >= writer.afterBytes {
		writer.mutate()
		writer.mutate = nil
	}
	return written, err
}

func TestDiskEvidenceExportReauthenticatesInspectorAfterStreaming(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, tt := range []struct {
			name string
			want e.Code
		}{
			{name: "unchanged"},
			{name: "inspector closed", want: e.CodeEngineCapability},
			{name: "profile substituted", want: e.CodeVerifyFailed},
			{name: "caller canceled", want: e.CodeTimeout},
		} {
			t.Run(architecture+"/"+tt.name, func(t *testing.T) {
				plan, owner := diskMaterialTraceFixtureForPlatform(t, false, false, "linux/"+architecture)
				trace, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
				if err != nil {
					t.Fatal(err)
				}
				bom, err := sbom.NewSubject(plan.LifecycleOutputs[0].BuildDigest, "image", trace.description.Subject.Digest)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := bom.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				inspection := &sbom.DiskInspectionRuntime{}
				profile := definition.DiskInspectionProfile{TimeoutSeconds: 30}
				receipt := &DiskEvidenceReceipt{owner: owner, runtime: inspection, trace: trace, profile: profile, root: raw}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				closed, calls := false, 0
				// Controlled profile observations model lifecycle changes; this
				// does not exercise real native inspector concurrency.
				bindings := func(ctx context.Context) (definition.DiskInspectionProfile, error) {
					calls++
					if err := ctx.Err(); err != nil {
						return definition.DiskInspectionProfile{}, e.Wrap(e.CodeTimeout, err, "export canceled")
					}
					if closed {
						return definition.DiskInspectionProfile{}, e.New(e.CodeEngineCapability, "inspector closed")
					}
					return profile, nil
				}
				writer := &diskEvidenceExportMutationWriter{mutate: func() {
					switch tt.name {
					case "inspector closed":
						closed = true
					case "profile substituted":
						profile.TimeoutSeconds++
					case "caller canceled":
						cancel()
					}
				}}
				if tt.name == "caller canceled" {
					// Cancel after the last write: no later copy iteration can
					// observe it, so final evidence admission must reject it.
					writer.afterBytes = int(trace.description.Subject.Size)
				}
				observed, document, err := owner.exportReconciledDisk(ctx, plan, plan.LifecycleOutputs[0], inspection, receipt, writer, bindings)
				if factoryErrorCode(err) != tt.want {
					t.Fatalf("export error=%v want %s", err, tt.want)
				}
				if writer.Len() == 0 {
					t.Fatal("mutation did not occur during payload streaming")
				}
				if tt.want != "" {
					if observed.Digest != "" || document != nil {
						t.Fatal("failed export returned authoritative observation or evidence")
					}
					if tt.name != "caller canceled" && calls != 2 {
						t.Fatalf("late runtime change was not reauthenticated: calls=%d", calls)
					}
					return
				}
				if calls != 2 || document == nil || observed.Digest != trace.description.Subject.Digest || !bytes.Equal(writer.Bytes(), testQCOW2HeaderPayload()) {
					t.Fatalf("unchanged export failed exact-byte/owner checks: calls=%d observed=%+v", calls, observed)
				}
			})
		}
	}
}

func TestDiskEvidenceCannotBeRestoredFromDescriptions(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixtureForPlatform(t, false, false, "linux/"+architecture)
			defer owner.Close()
			trace, err := owner.observeDiskMaterials(t.Context(), plan, plan.LifecycleOutputs[0])
			if err != nil {
				t.Fatal(err)
			}
			bom, err := sbom.NewSubject(plan.LifecycleOutputs[0].BuildDigest, "node", trace.description.Subject.Digest)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := bom.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			receipt := &DiskEvidenceReceipt{owner: owner, trace: trace, root: raw, documents: map[string][]byte{"node": raw}}
			view := receipt.ComponentDocuments()
			view["node"].Metadata.Component.Name = "changed"
			if receipt.ComponentDocuments()["node"].Metadata.Component.Name != "node" {
				t.Fatal("document accessor mutates receipt")
			}
			encoded, err := canonical.Marshal(receipt)
			if err != nil || string(encoded) != "{}" {
				t.Fatalf("receipt authority serialized: %s %v", encoded, err)
			}
			var destination bytes.Buffer
			for _, candidate := range []*DiskEvidenceReceipt{nil, {}, receipt} {
				_, _, err := owner.ExportReconciledDisk(t.Context(), plan, plan.LifecycleOutputs[0], &sbom.DiskInspectionRuntime{}, candidate, &destination)
				if factoryErrorCode(err) != e.CodeVerifyFailed || destination.Len() != 0 {
					t.Fatalf("unissued/substituted receipt admitted: %v", err)
				}
			}
		})
	}
}

func TestDiskEvidenceScannerUsesCompiledCanonicalToolPlatform(t *testing.T) {
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	definition.SBOMTool = "syft"
	pin := "sha256:" + strings.Repeat("9", 64)
	snapshot := *stack.Catalog
	snapshot.Tools = map[string]catalog.ToolPin{"syft": {
		Format: "raw", URL: "https://example.test/syft", Platform: "linux/amd64",
		ExecutablePath: "syft", ArchiveDigest: pin, ExecutableDigest: pin,
	}}
	stack, err := catalog.ApplyStack(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, platform, digest string
		want                   e.Code
	}{
		{name: "native exact scanner", platform: "linux/amd64", digest: pin},
		{name: "native substituted scanner", platform: "linux/amd64", digest: "sha256:" + strings.Repeat("8", 64), want: e.CodeVerifyFailed},
		{name: "other native host explicitly selected", platform: "linux/arm64", digest: "sha256:" + strings.Repeat("8", 64)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateDiskScannerSelection(plan, tt.platform, tt.digest); factoryErrorCode(err) != tt.want {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
	changed := *plan
	changed.EnvironmentDigest = "sha256:" + strings.Repeat("8", 64)
	if _, err := OpenDiskEvidenceRuntime(t.Context(), &changed, "does-not-exist", nil); factoryErrorCode(err) != e.CodeUnverifiedRef {
		t.Fatalf("tampered full Plan reached profile/runtime admission: %v", err)
	}
}

func TestDiskEvidenceAdmissionRequiresFrozenProfileBeforeRuntimeWork(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	defer owner.Close()
	missingRuntime := filepath.Join(t.TempDir(), plan.Environment.RuntimeBinary())
	if _, err := OpenDiskEvidenceRuntime(t.Context(), plan, missingRuntime, nil); factoryErrorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "no native disk inspection profile") {
		t.Fatalf("missing profile did not fail before runtime: %v", err)
	}
	if _, err := owner.ReconcileDiskEvidence(t.Context(), plan, plan.LifecycleOutputs[0], nil); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("missing admitted runtime accepted: %v", err)
	}
	if digest, size := (*DiskEvidenceReceipt)(nil).Subject(); digest != "" || size != 0 {
		t.Fatal("nil receipt carries subject")
	}
}

func TestDiskEvidenceRuntimeMatchesFrozenEnvironmentBeforeWork(t *testing.T) {
	for _, tt := range []struct {
		name, frozen, selected string
		matching               bool
	}{
		{name: "Docker exact", frozen: "docker", selected: "docker", matching: true},
		{name: "Podman exact", frozen: "podman", selected: "podman", matching: true},
		{name: "Docker absolute", frozen: "docker", selected: "/nonexistent/nkd-runtime/docker", matching: true},
		{name: "Podman absolute", frozen: "podman", selected: "/nonexistent/nkd-runtime/podman", matching: true},
		{name: "default Podman", selected: "/nonexistent/nkd-runtime/podman", matching: true},
		{name: "Docker substitutes Podman", frozen: "podman", selected: "docker"},
		{name: "Podman substitutes Docker", frozen: "docker", selected: "podman"},
		{name: "absolute wrong runtime", frozen: "docker", selected: "/nonexistent/nkd-runtime/podman"},
		{name: "default substitutes Docker", selected: "docker"},
		{name: "wrong name", frozen: "docker", selected: "does-not-exist"},
		{name: "empty name", frozen: "podman"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
			environment.Runtime.Runtime = tt.frozen
			plan, err := Compile(t.Context(), definition, environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			// No native profile and no workspace: a matching logical name
			// reaches the pure profile gate, never executable lookup or work.
			inspection, err := OpenDiskEvidenceRuntime(t.Context(), plan, tt.selected, nil)
			want := "disk inspector runtime differs from frozen Environment runtime"
			if tt.matching {
				want = "no native disk inspection profile"
			}
			if inspection != nil || factoryErrorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), want) {
				t.Fatalf("inspection=%v error=%v; expected admission failure %q", inspection, err, want)
			}
		})
	}
}
