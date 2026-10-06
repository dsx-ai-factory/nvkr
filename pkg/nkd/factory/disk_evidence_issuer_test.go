package factory

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestDiskEvidenceJoinDeadlineCountsContentOperations(t *testing.T) {
	for _, test := range []struct {
		name    string
		timeout uint64
		members int
		want    time.Duration
		code    e.Code
	}{
		{"root and capture", 1, 1, 2 * time.Second, ""},
		{"root two materials and capture", 600, 3, 40 * time.Minute, ""},
		{"missing timeout", 0, 3, 0, e.CodeEngineCapability},
		{"unbounded timeout", 601, 3, 0, e.CodeEngineCapability},
		{"missing root", 600, 0, 0, e.CodeVerifyFailed},
		{"invalid count", 600, -1, 0, e.CodeVerifyFailed},
		{"duration overflow", 600, math.MaxInt, 0, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := time.Unix(123, 0)
			deadline, err := diskEvidenceJoinDeadline(started, test.timeout, test.members)
			if factoryErrorCode(err) != test.code {
				t.Fatalf("deadline error=%v, want%s", err, test.code)
			}
			if test.code != "" {
				if !deadline.IsZero() {
					t.Fatal("rejected budget returned a deadline")
				}
			} else if !deadline.Equal(started.Add(test.want)) {
				t.Fatalf("deadline=%v, want%v", deadline, started.Add(test.want))
			}
		})
	}
}

func TestDiskEvidenceIssuerDeadlineUsesSelectedProfileAndOriginalCaller(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name    string
			seconds uint64
			caller  time.Duration
		}{
			{"long caller full profile", 600, time.Hour},
			{"long caller shorter profile", 60, time.Hour},
			{"short caller", 600, time.Minute},
		} {
			t.Run(architecture+"/"+test.name, func(t *testing.T) {
				plan, owner := diskMaterialTraceFixtureWithCapture(t, false, false, "linux/"+architecture, "none", "acquisitions.tar")
				// This private scan contract uses an owned, explicitly canceled
				// long parent, not the test runner's potentially shorter deadline.
				caller, cancel := context.WithTimeout(context.Background(), test.caller)
				defer cancel()
				profile := definition.DiskInspectionProfile{TimeoutSeconds: test.seconds}
				before, called := time.Now(), false
				_, err := owner.reconcileDiskEvidence(caller, plan, plan.LifecycleOutputs[0], &sbom.DiskInspectionRuntime{},
					func(context.Context) (definition.DiskInspectionProfile, error) { return profile, nil },
					func(ctx context.Context, _ BuildPlan, trace *diskMaterialTrace, _ *diskPayload, _ definition.DiskInspectionProfile) (diskEvidenceScan, error) {
						called = true
						deadline, ok := ctx.Deadline()
						if !ok {
							t.Fatal("issuer scan escaped its operation deadline")
						}
						allowance := time.Duration(test.seconds) * time.Second * time.Duration(len(trace.description.Members)+1)
						if test.caller < allowance {
							parent, _ := caller.Deadline()
							if !deadline.Equal(parent) {
								t.Fatal("join extended its original caller deadline")
							}
						} else if deadline.Before(before.Add(allowance)) || deadline.After(time.Now().Add(allowance)) {
							t.Fatalf("join deadline %v does not match selected profile %d for %d operations", deadline, test.seconds, len(trace.description.Members)+1)
						}
						return diskEvidenceScan{}, e.New(e.CodeEnvelopeDecode, "controlled stopped scan")
					})
				assertFactoryErrorCode(t, err, e.CodeEnvelopeDecode)
				if !called {
					t.Fatal("admitted modeled scan was not invoked")
				}
			})
		}
	}
}

func TestDiskEvidenceQueueAndAdmissionConsumeOriginalClock(t *testing.T) {
	for _, test := range []struct {
		name    string
		elapsed time.Duration
		queued  bool
		want    e.Code
	}{
		{"queue past ten minute bound", 11 * time.Minute, true, e.CodeTimeout},
		{"admission past ten minute bound", 10*time.Minute - time.Second, false, e.CodeTimeout},
		{"queue time consumes selected scan allowance", 9 * time.Minute, false, e.CodeEnvelopeDecode},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixtureWithCapture(t, false, false, "linux/amd64", "none", "acquisitions.tar")
			caller := t.Context()
			if test.queued {
				// If the queue deadline regresses, the caller watchdog releases
				// the blocked operation promptly and the assertion below fails.
				var cancel context.CancelFunc
				caller, cancel = context.WithTimeout(caller, time.Second)
				defer cancel()
				release, err := owner.acquireDiskPermit(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			started := time.Now().Add(-test.elapsed)
			bindingsCalled, scanned := false, false
			receipt, err := owner.reconcileDiskEvidenceStarted(caller, plan, plan.LifecycleOutputs[0], &sbom.DiskInspectionRuntime{},
				func(ctx context.Context) (definition.DiskInspectionProfile, error) {
					bindingsCalled = true
					deadline, ok := ctx.Deadline()
					if !ok || !deadline.Equal(started.Add(10*time.Minute)) {
						t.Fatalf("admission escaped original ten minute bound: %v", deadline)
					}
					if test.want == e.CodeTimeout {
						<-ctx.Done()
						return definition.DiskInspectionProfile{}, e.Wrap(e.CodeTimeout, ctx.Err(), "controlled expired admission")
					}
					return definition.DiskInspectionProfile{TimeoutSeconds: 600}, nil
				}, func(ctx context.Context, _ BuildPlan, trace *diskMaterialTrace, _ *diskPayload, _ definition.DiskInspectionProfile) (diskEvidenceScan, error) {
					scanned = true
					deadline, ok := ctx.Deadline()
					want := started.Add(time.Duration(len(trace.description.Members)+1) * 600 * time.Second)
					if !ok || !deadline.Equal(want) {
						t.Fatalf("join replenished queue/admission time: %v want%v", deadline, want)
					}
					return diskEvidenceScan{}, e.New(e.CodeEnvelopeDecode, "controlled stopped scan")
				}, started)
			if test.queued && caller.Err() != nil {
				t.Fatalf("queue exceeded short watchdog instead of its original deadline: %v", err)
			}
			assertFactoryErrorCode(t, err, test.want)
			if receipt != nil || bindingsCalled == test.queued || scanned != (test.want == e.CodeEnvelopeDecode) {
				t.Fatalf("expired operation performed work: bindings=%v scanned=%v", bindingsCalled, scanned)
			}
			if test.want == e.CodeTimeout {
				message := "controlled expired admission"
				if test.queued {
					message = "waiting to observe disk lifecycle payload"
				}
				if !strings.Contains(err.Error(), message) {
					t.Fatalf("wrong timeout owner: %v", err)
				}
			}
		})
	}
}

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
			{"substituted original trace binding", e.CodeVerifyFailed},
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
					bom.Metadata.Component.Type = "operating-system"
					bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "modeled-inventory", Version: "1"}}, "disk-reconciled/v1", "sha256:"+strings.Repeat("b", 64))
					if err != nil {
						return diskEvidenceScan{}, err
					}
					observed := diskEvidenceScan{subject: subject.Digest, size: subject.Size, profile: selected, bom: bom, documents: map[string][]byte{}}
					for _, member := range trace.description.Members {
						if member.Component == subject.Component {
							continue // Concrete scanning leaves the root for the issuer.
						}
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
					case "substituted original trace binding":
						// Corrupt only the private stopped scanner's trace view.
						// Re-observing the actual owner still yields the original
						// subject; the final ValidateSubject must reject this.
						trace.description.Subject.PlanDigest = "sha256:" + strings.Repeat("f", 64)
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
				rootDocument, err := documents[receipt.trace.description.Subject.Component].Marshal()
				if err != nil || !bytes.Equal(receipt.root, rootDocument) || !bytes.Contains(rootDocument, []byte("disk-reconciled/v1")) {
					t.Fatalf("issuer did not retain its reconciled root document: %v", err)
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
