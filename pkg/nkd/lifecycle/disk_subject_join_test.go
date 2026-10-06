package lifecycle

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

// These are descriptive join fixtures, not issued execution/inspection receipts.
func TestInstalledInventoryJoinsExplicitRawDiskSubject(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		wrapped bool
		change  func(*Artifact, *factory.DiskInstalledInventoryDescription)
		code    e.Code
	}{
		{name: "local raw deliverable"},
		{name: "wrapped deliverable", wrapped: true},
		{name: "missing raw pin", change: func(output *Artifact, _ *factory.DiskInstalledInventoryDescription) { output.Disk.PayloadDigest = "" }, code: e.CodeVerifyFailed},
		{name: "changed raw pin", wrapped: true, change: func(output *Artifact, _ *factory.DiskInstalledInventoryDescription) {
			output.Disk.PayloadDigest = output.Digest
		}, code: e.CodeVerifyFailed},
		{name: "wrapper inventory", wrapped: true, change: func(output *Artifact, inventory *factory.DiskInstalledInventoryDescription) {
			inventory.Subject.Digest = output.Digest
		}, code: e.CodeVerifyFailed},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			raw := "sha256:" + strings.Repeat("a", 64)
			build := "sha256:" + strings.Repeat("b", 64)
			plan := "sha256:" + strings.Repeat("c", 64)
			output := Artifact{Name: "node-image", Kind: OutputDiskImage, Digest: raw, BuildDigest: build,
				Components: []string{"image"}, Disk: &delivery.DiskOutput{PayloadDigest: raw, Producer: "build-image", Component: "image",
					Format: "qcow2", Encryption: "none", Size: 4096, PlanDigest: plan, Outcome: "succeeded"}}
			if scenario.wrapped {
				output.Digest = "sha256:" + strings.Repeat("d", 64)
			}
			inventory := factory.DiskInstalledInventoryDescription{OutputName: output.Name, Subject: factory.DiskPayloadObservation{
				Digest: raw, Size: output.Disk.Size, BuildDigest: build, PlanDigest: plan, Format: "qcow2", Encryption: "none"}}
			if scenario.change != nil {
				scenario.change(&output, &inventory)
			}
			run := &Run{PlanDigest: plan, Observed: []Artifact{output}, diskInventories: []factory.DiskInstalledInventoryDescription{inventory}}
			err := validateInstalledInventorySubjects(run)
			if scenario.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, scenario.code)
			}
		})
	}
}
