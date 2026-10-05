package factory

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// This invokes the production issuer core, never a receipt literal. Private
// scan/profile observations model already-stopped operations; they do not prove
// native Packer, opaque content discovery, or the public end-to-end runtime.
func TestDiskEvidenceIssuerCoreOwnsCompleteJoin(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name string
			want e.Code
		}{
			{"issued", ""},
			{"closed owner", e.CodeGateUnmet},
			{"encrypted subject", e.CodeEncryptedArtifact},
			{"sibling output", e.CodeVerifyFailed},
			{"missing edge before scan", e.CodeVerifyFailed},
			{"subject substitution", e.CodeVerifyFailed},
			{"size substitution", e.CodeVerifyFailed},
			{"scan profile substitution", e.CodeVerifyFailed},
			{"late profile substitution", e.CodeVerifyFailed},
			{"late profile revoked", e.CodeEngineCapability},
			{"missing component documents", e.CodeVerifyFailed},
			{"invalid inventory document", e.CodeInvalidCatalog},
			{"missing original material", e.CodeVerifyFailed},
			{"changed material edge", e.CodeVerifyFailed},
			{"changed original payload", e.CodeVerifyFailed},
			{"scan failure", e.CodeEnvelopeDecode},
			{"cancel before admission", e.CodeTimeout},
			{"cancel during scan", e.CodeTimeout},
			{"close during scan", e.CodeTimeout},
		} {
			t.Run(architecture+"/"+test.name, func(t *testing.T) {
				encryption := "none"
				if test.name == "encrypted subject" {
					encryption = "luks2"
				}
				plan, owner := diskMaterialTraceFixtureWithCapture(t, true, false, "linux/"+architecture, encryption, "acquisitions.tar")
				expected := plan.LifecycleOutputs[0]
				inspection := &sbom.DiskInspectionRuntime{}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				profile := definition.DiskInspectionProfile{Image: "registry.example/inspect@sha256:" + strings.Repeat("a", 64), ScratchStorage: "private-volume", TimeoutSeconds: 600}
				bindingCalls, scanCalls := 0, 0
				var originalPayload *diskPayload
				for _, completion := range owner.diskCompletions {
					if completion.NodeID == "node-image/build-image" {
						originalPayload = completion.Payload
					}
				}
				bindings := func(context.Context) (definition.DiskInspectionProfile, error) {
					bindingCalls++
					if bindingCalls == 2 {
						switch test.name {
						case "late profile substitution":
							profile.TimeoutSeconds--
						case "late profile revoked":
							return definition.DiskInspectionProfile{}, e.New(e.CodeEngineCapability, "inspector revoked")
						}
					}
					return profile, nil
				}
				var closeDone chan error
				scan := func(ctx context.Context, build BuildPlan, trace *diskMaterialTrace, payload *diskPayload, selected definition.DiskInspectionProfile) (diskEvidenceScan, error) {
					scanCalls++
					if payload == nil || payload != originalPayload || build.Platform != "linux/"+architecture || selected != profile || len(trace.description.Edges) != 3 {
						t.Fatal("issuer did not pass its original payload, native consumer and complete trace")
					}
					subject := trace.description.Subject
					bom, err := sbom.NewSubject(build.BuildDigest, subject.Component, subject.Digest)
					if err != nil {
						return diskEvidenceScan{}, err
					}
					bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "modeled-inventory", Version: "1"}}, "disk-reconciled/v1", "sha256:"+strings.Repeat("b", 64))
					if err != nil {
						return diskEvidenceScan{}, err
					}
					observed := diskEvidenceScan{subject: subject.Digest, size: subject.Size, profile: selected, bom: bom, documents: map[string][]byte{}}
					for _, member := range trace.description.Members {
						document, err := sbom.NewSubject(build.BuildDigest, member.Component, member.Digest)
						if err != nil {
							return diskEvidenceScan{}, err
						}
						raw, err := document.Marshal()
						if err != nil {
							return diskEvidenceScan{}, err
						}
						observed.documents[member.Component] = raw
					}
					switch test.name {
					case "subject substitution":
						observed.subject = "sha256:" + strings.Repeat("f", 64)
					case "size substitution":
						observed.size++
					case "scan profile substitution":
						observed.profile.TimeoutSeconds--
					case "missing component documents":
						observed.documents = nil
					case "invalid inventory document":
						observed.bom = &sbom.BOM{}
					case "missing original material":
						delete(owner.materialConsumptions, "node-image/pull-runtime")
					case "changed material edge":
						owner.materialConsumptions["node-image/build-agent"].materials[0].Binding = "substituted"
					case "changed original payload":
						fakeDiskPayloadState(payload).payload[0] ^= 1
					case "scan failure":
						return diskEvidenceScan{}, e.New(e.CodeEnvelopeDecode, "scan failed")
					case "cancel during scan":
						cancel()
					case "close during scan":
						closeDone = make(chan error, 1)
						go func() { closeDone <- owner.Close() }()
						select {
						case <-ctx.Done():
						case <-time.After(5 * time.Second):
							t.Fatal("Close did not cancel the registered issuer operation")
						}
					}
					return observed, nil
				}
				switch test.name {
				case "closed owner":
					if err := owner.Close(); err != nil {
						t.Fatal(err)
					}
				case "sibling output":
					expected = plan.LifecycleOutputs[1]
				case "missing edge before scan":
					consumption := owner.materialConsumptions["node-image/build-agent"]
					consumption.materials = nil
					owner.materialConsumptions["node-image/build-agent"] = consumption
				case "cancel before admission":
					cancel()
				}
				receipt, err := owner.reconcileDiskEvidence(ctx, plan, expected, inspection, bindings, scan)
				if closeDone != nil {
					select {
					case err := <-closeDone:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("issuer did not release owner cleanup")
					}
				}
				if factoryErrorCode(err) != test.want {
					t.Fatalf("issuer error=%v, want %s (scans=%d)", err, test.want, scanCalls)
				}
				if test.want != "" {
					if receipt != nil {
						t.Fatal("rejected join issued authority")
					}
					return
				}
				if receipt == nil || receipt.owner != owner || receipt.runtime != inspection || receipt.trace == nil || bindingCalls != 2 || scanCalls != 1 {
					t.Fatal("successful core did not issue an original-owner receipt")
				}
				documents, err := receipt.ComponentDocuments()
				if err != nil || len(documents) != 3 {
					t.Fatalf("issued documents: %v %v", documents, err)
				}
				var destination bytes.Buffer
				observed, document, err := owner.exportReconciledDisk(ctx, plan, expected, inspection, receipt, &destination, bindings)
				if err != nil || document == nil || observed.Digest != receipt.trace.description.Subject.Digest || !bytes.Equal(destination.Bytes(), testQCOW2HeaderPayload()) {
					t.Fatalf("issued receipt failed original-owner export: %v", err)
				}
				otherPlan, otherOwner := diskMaterialTraceFixtureWithCapture(t, true, false, "linux/"+architecture, "none", "acquisitions.tar")
				var rejected bytes.Buffer
				_, document, err = otherOwner.exportReconciledDisk(ctx, otherPlan, otherPlan.LifecycleOutputs[0], inspection, receipt, &rejected, bindings)
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				if document != nil || rejected.Len() != 0 {
					t.Fatal("equal-byte sibling Run reused issued receipt")
				}
				// The public boundary never accepts the private modeled inspector.
				if publicReceipt, err := owner.ReconcileDiskEvidence(ctx, plan, expected, inspection); publicReceipt != nil || factoryErrorCode(err) != e.CodeEngineCapability {
					t.Fatalf("public API accepted modeled inspection: %v", err)
				}
			})
		}
	}
}
