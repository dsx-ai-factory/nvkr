package factory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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

func TestLiveDiskInspectionCloseCancelsAndJoinsScan(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	started := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		receipt, err := owner.observeDiskInstalledInventory(t.Context(), plan, plan.LifecycleOutputs[0], &sbom.DiskInspectionRuntime{}, testDiskInspectionBindings,
			func(ctx context.Context, _ *diskPayload, _, _ string) (diskInstalledScan, error) {
				close(started)
				<-ctx.Done()
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
