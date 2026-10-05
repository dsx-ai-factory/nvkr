package factory

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func testDiskInspectionBindings(context.Context) (diskInspectionBindings, error) {
	return diskInspectionBindings{image: "sha256:" + strings.Repeat("a", 64),
		inspector: "sha256:" + strings.Repeat("b", 64), codec: "sha256:" + strings.Repeat("c", 64)}, nil
}

func testDiskInventoryScan(ctx context.Context, payload *diskPayload, format, encryption string) (diskInstalledScan, error) {
	if format != "qcow2" || encryption != "none" {
		return diskInstalledScan{}, e.New(e.CodeEngineCapability, "fixture requires plain QCOW2")
	}
	bindings, _ := testDiskInspectionBindings(ctx)
	return diskInstalledScan{subject: payload.digest, size: payload.size, bindings: bindings,
		packages: []sbom.InstalledDiskPackage{{Name: "fixture-package", Version: "1", Format: "deb"}}}, nil
}

func TestLiveDiskInventoryObservationAdmissionCodes(t *testing.T) {
	for _, test := range []struct {
		name string
		want e.Code
	}{
		{"nil context", e.CodeUsage}, {"nil owner", e.CodeUsage},
		{"nil bindings", e.CodeUsage}, {"nil scan", e.CodeUsage},
		{"closed Run", e.CodeGateUnmet}, {"bindings failure", e.CodeEngineCapability},
		{"non-digest image", e.CodeUnverifiedRef}, {"non-digest inspector", e.CodeUnverifiedRef},
		{"non-digest codec", e.CodeUnverifiedRef}, {"post-scan bindings failure", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixture(t)
			ctx := t.Context()
			bindings := diskInventoryBindings(testDiskInspectionBindings)
			scan := diskInventoryScan(testDiskInventoryScan)
			switch test.name {
			case "nil context":
				ctx = nil
			case "nil owner":
				owner = nil
			case "nil bindings":
				bindings = nil
			case "nil scan":
				scan = nil
			case "closed Run":
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			default:
				calls := 0
				bindings = func(ctx context.Context) (diskInspectionBindings, error) {
					value, err := testDiskInspectionBindings(ctx)
					calls++
					switch test.name {
					case "bindings failure":
						err = e.New(e.CodeEngineCapability, "fixture binding failure")
					case "post-scan bindings failure":
						if calls > 1 {
							err = e.New(e.CodeEngineCapability, "fixture post-scan binding failure")
						}
					case "non-digest image":
						value.image = "image:tag"
					case "non-digest inspector":
						value.inspector = "inspector"
					case "non-digest codec":
						value.codec = "codec"
					}
					return value, err
				}
			}
			receipt, err := owner.observeDiskInstalledInventory(ctx, plan, plan.LifecycleOutputs[0], &sbom.DiskInspectionRuntime{}, bindings, scan)
			if receipt != nil || factoryErrorCode(err) != test.want {
				t.Fatalf("receipt=%v error=%v, want %s", receipt, err, test.want)
			}
		})
	}
}

func TestLiveDiskInventoryValidationCodes(t *testing.T) {
	for _, test := range []struct {
		name string
		want e.Code
	}{
		{"nil context", e.CodeUsage}, {"nil bindings", e.CodeVerifyFailed},
		{"nil trace", e.CodeVerifyFailed}, {"closed Run", e.CodeGateUnmet},
		{"binding failure", e.CodeEngineCapability}, {"changed image", e.CodeVerifyFailed},
		{"changed inspector", e.CodeVerifyFailed}, {"changed codec", e.CodeVerifyFailed},
		{"changed recorded size", e.CodeVerifyFailed}, {"changed recorded output", e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixture(t)
			inspector := &sbom.DiskInspectionRuntime{}
			output := plan.LifecycleOutputs[0]
			receipt, err := owner.observeDiskInstalledInventory(t.Context(), plan, output, inspector, testDiskInspectionBindings, testDiskInventoryScan)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			bindings := diskInventoryBindings(testDiskInspectionBindings)
			switch test.name {
			case "nil context":
				ctx = nil
			case "nil bindings":
				bindings = nil
			case "nil trace":
				receipt.trace = nil
			case "closed Run":
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			case "changed recorded size":
				receipt.description.Subject.Size++
			case "changed recorded output":
				receipt.description.OutputName = "sibling"
			default:
				bindings = func(ctx context.Context) (diskInspectionBindings, error) {
					value, err := testDiskInspectionBindings(ctx)
					switch test.name {
					case "binding failure":
						err = e.New(e.CodeEngineCapability, "fixture binding failure")
					case "changed image":
						value.image = "sha256:" + strings.Repeat("f", 64)
					case "changed inspector":
						value.inspector = "sha256:" + strings.Repeat("f", 64)
					case "changed codec":
						value.codec = "sha256:" + strings.Repeat("f", 64)
					}
					return value, err
				}
			}
			if err := owner.validateDiskInstalledInventory(ctx, plan, output, inspector, receipt, bindings); factoryErrorCode(err) != test.want {
				t.Fatalf("error=%v, want %s", err, test.want)
			}
		})
	}
}

func TestLiveDiskInstalledInventoryBindsBothArchitectures(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixtureForPlatform(t, false, false, platform)
			inspector := &sbom.DiskInspectionRuntime{}
			output := plan.LifecycleOutputs[0]
			receipt, err := owner.observeDiskInstalledInventory(t.Context(), plan, output, inspector, testDiskInspectionBindings, testDiskInventoryScan)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.validateDiskInstalledInventory(t.Context(), plan, output, inspector, receipt, testDiskInspectionBindings); err != nil {
				t.Fatal(err)
			}
			view := receipt.Description()
			if view.OutputName != output.Name || view.Subject.BuildDigest != output.BuildDigest ||
				view.Subject.PlanDigest != owner.PlanDigest || view.Subject.Size != owner.diskCompletions[0].Payload.size || len(view.Packages) != 1 {
				t.Fatalf("incomplete exact binding: %+v", view)
			}
			view.Packages[0].Name = "changed"
			view.Subject.Components[0] = "changed"
			if again := receipt.Description(); again.Packages[0].Name == "changed" || again.Subject.Components[0] == "changed" {
				t.Fatal("description mutated issued receipt")
			}
			raw, err := canonical.Marshal(receipt)
			if err != nil || string(raw) != "{}" {
				t.Fatalf("receipt escaped serialization: %s, %v", raw, err)
			}
			copyReceipt := *receipt
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := owner.validateDiskInstalledInventory(t.Context(), plan, output, inspector, &copyReceipt, testDiskInspectionBindings); factoryErrorCode(err) != e.CodeGateUnmet {
				t.Fatalf("shallow copy retained live authority after close: %v", err)
			}
			if len(receipt.Description().Packages) != 1 {
				t.Fatal("Close discarded immutable historical description")
			}
		})
	}
}

func TestLiveDiskInstalledInventoryRejectsSubstitution(t *testing.T) {
	for _, name := range []string{"wrong output", "missing material", "changed disk", "wrong subject", "wrong size", "wrong inspector", "wrong codec", "wrong runtime", "runtime changes mid scan", "empty inventory"} {
		t.Run(name, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixture(t)
			output := plan.LifecycleOutputs[0]
			scan := testDiskInventoryScan
			bindings := diskInventoryBindings(testDiskInspectionBindings)
			switch name {
			case "wrong output":
				output.Name = "sibling"
			case "missing material":
				delete(owner.materialConsumptions, "node-image/pull-runtime")
			case "runtime changes mid scan":
				calls := 0
				bindings = func(ctx context.Context) (diskInspectionBindings, error) {
					value, err := testDiskInspectionBindings(ctx)
					calls++
					if calls > 1 {
						value.image = "sha256:" + strings.Repeat("f", 64)
					}
					return value, err
				}
			default:
				scan = func(ctx context.Context, payload *diskPayload, format, encryption string) (diskInstalledScan, error) {
					value, err := testDiskInventoryScan(ctx, payload, format, encryption)
					switch name {
					case "changed disk":
						fakeDiskPayloadState(payload).payload[200] ^= 1
					case "wrong subject":
						value.subject = "sha256:" + strings.Repeat("f", 64)
					case "wrong size":
						value.size++
					case "wrong inspector":
						value.bindings.inspector = "sha256:" + strings.Repeat("f", 64)
					case "wrong codec":
						value.bindings.codec = "sha256:" + strings.Repeat("f", 64)
					case "wrong runtime":
						value.bindings.image = "sha256:" + strings.Repeat("f", 64)
					case "empty inventory":
						value.packages = nil
					}
					return value, err
				}
			}
			receipt, err := owner.observeDiskInstalledInventory(t.Context(), plan, output, &sbom.DiskInspectionRuntime{}, bindings, scan)
			if receipt != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("substitution issued receipt: %v, %v", receipt, err)
			}
		})
	}
}

func TestLiveDiskInstalledInventoryCannotReplayEqualByteSiblingAttempt(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	inspector := &sbom.DiskInspectionRuntime{}
	output := plan.LifecycleOutputs[0]
	receipt, err := owner.observeDiskInstalledInventory(t.Context(), plan, output, inspector, testDiskInspectionBindings, testDiskInventoryScan)
	if err != nil {
		t.Fatal(err)
	}
	// An otherwise equal Plan/Build/subject cannot change the issuing owner.
	sibling := localDiskRunResult(t, plan)
	defer sibling.Close()
	if err := sibling.validateDiskInstalledInventory(t.Context(), plan, output, inspector, receipt, testDiskInspectionBindings); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("equal-byte sibling reused receipt: %v", err)
	}
	if err := owner.validateDiskInstalledInventory(t.Context(), plan, output, &sbom.DiskInspectionRuntime{}, receipt, testDiskInspectionBindings); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("substituted inspector reused receipt: %v", err)
	}
	if err := owner.validateDiskInstalledInventory(t.Context(), plan, output, inspector, &DiskInstalledInventoryReceipt{}, testDiskInspectionBindings); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("caller-composed receipt admitted: %v", err)
	}
}

func TestLiveDiskInstalledInventoryCannotReplayEqualByteSiblingOutput(t *testing.T) {
	plan, owner := diskMaterialTraceFixtureWithSibling(t, true)
	inspector := &sbom.DiskInspectionRuntime{}
	original := plan.LifecycleOutputs[0]
	var sibling LifecycleOutputPlan
	var operation Node
	for _, output := range plan.LifecycleOutputs {
		if output.Name == "sibling-image" {
			sibling = output
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.BuildTarget == sibling.Name && node.IRNode == "build-image" {
			operation = node
		}
	}
	if sibling.BuildDigest != original.BuildDigest || operation.ID == "" {
		t.Fatal("fixture must have two real outputs with equal logical Build identities")
	}
	// Model the sibling's own successful invocations, not a missing-material
	// rejection: both traces and held completions must be independently valid.
	for id, consumption := range maps.Clone(owner.materialConsumptions) {
		consumption.nodeID = strings.Replace(id, original.Name+"/", sibling.Name+"/", 1)
		owner.materialConsumptions[consumption.nodeID] = consumption
	}
	completion := owner.diskCompletions[0]
	content := fakeDiskPayloadState(completion.Payload)
	held := &fakeDiskProducerResult{payload: append([]byte(nil), content.payload...), digest: content.digest, size: content.size}
	if err := populateHeldDiskProducerCompletion(t.Context(), plan, operation.ID, owner,
		func(context.Context, ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
			return fakeDiskPayload(held), &completion.Result, nil
		}); err != nil {
		t.Fatal(err)
	}
	owner.Nodes = append(owner.Nodes, LocalNodeResult{ID: operation.ID, Kind: NodeBuild, Status: "ok"})
	issued := make(map[string]*DiskInstalledInventoryReceipt)
	for _, output := range []LifecycleOutputPlan{original, sibling} {
		receipt, err := owner.observeDiskInstalledInventory(t.Context(), plan, output, inspector, testDiskInspectionBindings, testDiskInventoryScan)
		if err != nil {
			t.Fatal(err)
		}
		if err := owner.validateDiskInstalledInventory(t.Context(), plan, output, inspector, receipt, testDiskInspectionBindings); err != nil {
			t.Fatal(err)
		}
		issued[output.Name] = receipt
	}
	if issued[original.Name].Description().Subject.Digest != issued[sibling.Name].Description().Subject.Digest {
		t.Fatal("fixture must have identical delivered bytes")
	}
	for _, output := range []LifecycleOutputPlan{original, sibling} {
		other := issued[original.Name]
		if output.Name == original.Name {
			other = issued[sibling.Name]
		}
		if err := owner.validateDiskInstalledInventory(t.Context(), plan, output, inspector, other, testDiskInspectionBindings); factoryErrorCode(err) != e.CodeVerifyFailed {
			t.Fatalf("receipt replay to real sibling output %q: %v", output.Name, err)
		}
	}
}

func TestLiveDiskInspectionDeadlineBoundsCompleteOperation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		parent time.Duration
	}{
		{name: "shorter caller", parent: time.Minute},
		{name: "longer caller", parent: 20 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, owner := diskMaterialTraceFixture(t)
			parent, cancel := context.WithTimeout(t.Context(), tt.parent)
			defer cancel()
			started := time.Now()
			ctx, release, err := owner.beginDiskInspection(parent)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("inspection operation has no deadline")
			}
			if tt.parent < 10*time.Minute {
				callerDeadline, _ := parent.Deadline()
				if !deadline.Equal(callerDeadline) {
					t.Fatal("inspection extended the shorter caller deadline")
				}
			} else if deadline.Before(started.Add(10*time.Minute)) || deadline.After(time.Now().Add(10*time.Minute)) {
				t.Fatal("complete handoff escaped its ten-minute ceiling")
			}
			cancel()
			if ctx.Err() != context.Canceled {
				t.Fatal("inspection did not retain caller cancellation")
			}
		})
	}
}

func TestLiveDiskInspectionCloseCancelsAndJoinsScan(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	started := make(chan struct{})
	canceled := make(chan struct{})
	finish := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		receipt, err := owner.observeDiskInstalledInventory(t.Context(), plan, plan.LifecycleOutputs[0], &sbom.DiskInspectionRuntime{}, testDiskInspectionBindings,
			func(ctx context.Context, _ *diskPayload, _, _ string) (diskInstalledScan, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-finish
				return diskInstalledScan{}, ctx.Err()
			})
		if receipt != nil {
			returned <- e.New(e.CodeInternal, "canceled scan issued receipt")
			return
		}
		returned <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- owner.Close() }()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		close(finish)
		t.Fatal("Close did not cancel the active scan")
	}
	select {
	case err := <-closed:
		close(finish)
		t.Fatalf("Close returned before scan cleanup finished: %v", err)
	default:
	}
	close(finish)
	select {
	case err := <-returned:
		if factoryErrorCode(err) != e.CodeTimeout {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel the active scan")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join canceled scan")
	}
	if !owner.diskClosed || len(owner.diskCompletions) != 0 || len(owner.materialConsumptions) != 0 {
		t.Fatal("Close retained live disk state")
	}
}

func TestLiveDiskInspectionCancellationPreservesCleanupFailure(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cleanupErr := errors.New("fixture cleanup failure")
	receipt, err := owner.observeDiskInstalledInventory(ctx, plan, plan.LifecycleOutputs[0],
		&sbom.DiskInspectionRuntime{}, testDiskInspectionBindings,
		func(context.Context, *diskPayload, string, string) (diskInstalledScan, error) {
			cancel()
			return diskInstalledScan{}, e.Wrap(e.CodeInternal, cleanupErr, "removing inspection container")
		})
	if receipt != nil || factoryErrorCode(err) != e.CodeTimeout ||
		!errors.Is(err, context.Canceled) || !errors.Is(err, cleanupErr) {
		t.Fatalf("cancellation lost cleanup failure or issued a receipt: %v, %v", receipt, err)
	}
}
