package factory

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func correspondenceCapture(t *testing.T, architecture string) diskAcquisitionScan {
	t.Helper()
	payload := []byte("captured package bytes, not validated package metadata")
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	manifest := material.AcquisitionManifest{SchemaVersion: material.AcquisitionSchemaVersion,
		Architecture: architecture, Scope: "apt-pre-install-pkgs", Packages: []material.AcquiredPackage{{
			Name: "fixture-package", Version: "1:2.0-1", Architecture: architecture,
			Digest: "sha256:" + digest, Size: int64(len(payload)), Path: "packages/" + digest + ".deb",
			RepositoryURIs: []string{"https://example.invalid/pool/fixture.deb"}}}}
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range []struct {
		name    string
		content []byte
	}{{"manifest.json", raw}, {manifest.Packages[0].Path, payload}} {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.content)), Mode: 0o444, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	held, err := heldfile.FromBytesContext(t.Context(), buffer.Bytes(), heldfile.Options{Label: "capture-fixture", MaxBytes: material.MaxAcquisitionArchiveBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	return func(ctx context.Context, _ *diskPayload) (*material.AcquisitionObservation, error) {
		return material.ObserveAcquisitionArchive(ctx, held)
	}
}

func TestDiskPackageCorrespondenceCloseCancelsAndJoinsCapture(t *testing.T) {
	plan, owner := diskMaterialTraceFixture(t)
	runtime := &sbom.DiskInspectionRuntime{}
	inventory := correspondenceInventory(t, plan, owner, runtime, "amd64")
	started := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		receipt, err := owner.observeDiskPackageCorrespondence(t.Context(), plan, plan.LifecycleOutputs[0], runtime, inventory, testDiskInspectionBindings,
			func(ctx context.Context, _ *diskPayload) (*material.AcquisitionObservation, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			})
		if receipt != nil {
			returned <- e.New(e.CodeInternal, "canceled capture issued receipt")
			return
		}
		returned <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- owner.Close() }()
	select {
	case err := <-returned:
		if factoryErrorCode(err) != e.CodeTimeout {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel capture")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join capture")
	}
}

func correspondenceInventory(t *testing.T, plan *Plan, owner *LocalRunResult, runtime *sbom.DiskInspectionRuntime, architecture string) *DiskInstalledInventoryReceipt {
	t.Helper()
	scan := func(ctx context.Context, payload *diskPayload, format, encryption string) (diskInstalledScan, error) {
		value, err := testDiskInventoryScan(ctx, payload, format, encryption)
		value.packages[0].Architecture = architecture
		value.packages[0].Version = "2.0"
		value.packages[0].Release = "1"
		value.packages[0].Epoch = "1"
		return value, err
	}
	receipt, err := owner.observeDiskInstalledInventory(t.Context(), plan, plan.LifecycleOutputs[0], runtime, testDiskInspectionBindings, scan)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestDiskPackageCorrespondenceBindsBothArchitecturesWithoutCompletenessClaim(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixtureForPlatform(t, false, false, "linux/"+architecture)
			runtime := &sbom.DiskInspectionRuntime{}
			inventory := correspondenceInventory(t, plan, owner, runtime, architecture)
			scan := correspondenceCapture(t, architecture)
			output := plan.LifecycleOutputs[0]
			receipt, err := owner.observeDiskPackageCorrespondence(t.Context(), plan, output, runtime, inventory, testDiskInspectionBindings, scan)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.validateDiskPackageCorrespondence(t.Context(), plan, output, runtime, receipt, testDiskInspectionBindings, scan); err != nil {
				t.Fatal(err)
			}
			view := receipt.Description()
			if view.Inventory.Subject.PlanDigest != owner.PlanDigest || view.ArchiveSize <= 0 || len(view.Candidates) != 1 || len(view.Candidates[0].Captured) != 1 || len(view.CoverageGaps) != 3 {
				t.Fatalf("incomplete binding: %+v", view)
			}
			if view.Candidates[0].Installed.Version != "2.0" || view.Candidates[0].Captured[0].Version != "1:2.0-1" {
				t.Fatal("version metadata was normalized")
			}
			view.Capture.Packages[0].RepositoryURIs[0] = "changed"
			view.Candidates[0].Captured[0].Name = "changed"
			view.CoverageGaps[0] = "changed"
			if again := receipt.Description(); again.Capture.Packages[0].RepositoryURIs[0] == "changed" || again.Candidates[0].Captured[0].Name == "changed" || again.CoverageGaps[0] == "changed" {
				t.Fatal("description mutation changed receipt")
			}
			raw, err := canonical.Marshal(receipt)
			if err != nil || string(raw) != "{}" {
				t.Fatalf("serialized receipt: %s, %v", raw, err)
			}
			copyReceipt := *receipt
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := owner.validateDiskPackageCorrespondence(t.Context(), plan, output, runtime, &copyReceipt, testDiskInspectionBindings, scan); factoryErrorCode(err) != e.CodeGateUnmet {
				t.Fatalf("close did not revoke copy: %v", err)
			}
		})
	}
}

func TestDiskPackageCorrespondenceRejectsSubstitution(t *testing.T) {
	for _, name := range []string{"wrong output", "sibling attempt", "wrong inspector", "wrong capture architecture", "wrong installed architecture", "changed disk", "missing carrier", "canceled"} {
		t.Run(name, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixture(t)
			runtime := &sbom.DiskInspectionRuntime{}
			inventory := correspondenceInventory(t, plan, owner, runtime, "amd64")
			scan := correspondenceCapture(t, "amd64")
			output := plan.LifecycleOutputs[0]
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch name {
			case "wrong output":
				output.Name = "sibling"
			case "sibling attempt":
				_, owner = diskMaterialTraceFixture(t)
			case "wrong inspector":
				runtime = &sbom.DiskInspectionRuntime{}
			case "wrong capture architecture":
				scan = correspondenceCapture(t, "arm64")
			case "wrong installed architecture":
				inventory = correspondenceInventory(t, plan, owner, runtime, "arm64")
			case "changed disk":
				fakeDiskPayloadState(owner.diskCompletions[0].Payload).payload[200] ^= 1
			case "missing carrier":
				scan = func(context.Context, *diskPayload) (*material.AcquisitionObservation, error) {
					return nil, e.New(e.CodeGateUnmet, "no capture")
				}
			case "canceled":
				cancel()
			}
			receipt, err := owner.observeDiskPackageCorrespondence(ctx, plan, output, runtime, inventory, testDiskInspectionBindings, scan)
			want := e.CodeVerifyFailed
			if name == "missing carrier" {
				want = e.CodeGateUnmet
			}
			if name == "canceled" {
				want = e.CodeTimeout
			}
			if receipt != nil || factoryErrorCode(err) != want {
				t.Fatalf("substitution issued receipt: %v,%v, want %s", receipt, err, want)
			}
		})
	}
}
