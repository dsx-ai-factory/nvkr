package lifecycle_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// TestNKEOSLiveInstalledInventory is deliberately opt-in: it executes a complete
// NKE bake, not a replay of a retained image. Run separately on native AMD64 and
// ARM64 hosts with explicit measured runtime pins/resources and a long test
// timeout. The ordinary contract gate never starts an expensive image build.
func TestNKEOSLiveInstalledInventory(t *testing.T) {
	if os.Getenv("NKD_TEST_NKE_LIVE_INSPECTION") != "1" {
		t.Skip("set NKD_TEST_NKE_LIVE_INSPECTION=1 for the real original-Run bake/inspection")
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Fatal("real live inspection requires native Linux AMD64 or ARM64")
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	workspace, err := os.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	selection := pipeline.DiskInspectionSelection{
		RuntimeExecutable: requiredLiveInspectionValue(t, "NKD_TEST_INSPECT_RUNTIME"),
		Image:             requiredLiveInspectionValue(t, "NKD_TEST_INSPECT_IMAGE"),
		Platform:          "linux/" + runtime.GOARCH,
		InspectorDigest:   requiredLiveInspectionValue(t, "NKD_TEST_INSPECT_INSPECTOR_DIGEST"),
		CodecDigest:       requiredLiveInspectionValue(t, "NKD_TEST_INSPECT_CODEC_DIGEST"),
		Resources: execution.ResourceRequest{
			CPUMillis:           requiredLiveInspectionNumber(t, "NKD_TEST_INSPECT_CPU_MILLIS", 64),
			MemoryMiB:           requiredLiveInspectionNumber(t, "NKD_TEST_INSPECT_MEMORY_MIB", 64),
			EphemeralStorageMiB: requiredLiveInspectionNumber(t, "NKD_TEST_INSPECT_STORAGE_MIB", 64),
		},
		TimeoutSeconds: 600, Workspace: workspace,
	}
	inspector, err := sbom.OpenDiskInspectionRuntime(t.Context(), selection)
	if err != nil {
		t.Fatal(err)
	}
	defer inspector.Close()
	definitionPath := requiredLiveInspectionValue(t, "NKD_TEST_NKE_DEFINITION")
	definition, err := lifecycle.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(definition.Outputs) != 1 || definition.Outputs[0].Format != lifecycle.OutputDiskImage ||
		definition.Outputs[0].Platform != selection.Platform {
		t.Fatal("real live inspection requires one disk output targeting the actual native architecture")
	}
	definitionDigest, err := definition.Digest()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := lifecycle.PrepareFiles(t.Context(), definitionPath,
		requiredLiveInspectionValue(t, "NKD_TEST_NKE_ENVIRONMENT"), "live-inspection-test", "inspection")
	if err != nil {
		t.Fatal(err)
	}
	expectedRun := prepared.Description()
	if expectedRun.DefinitionDigest != definitionDigest {
		t.Fatal("the prepared Run must bind the exact native-architecture Definition checked above")
	}
	uniqueRoot := strings.ToLower(filepath.Base(filepath.Dir(t.TempDir())))
	run, err := lifecycle.ExecuteLocal(t.Context(), prepared, lifecycle.LocalOptions{
		AttemptsRoot: filepath.Join(".nkd", "live-inspection", uniqueRoot), RunnerSource: repo,
		Runtime: selection.RuntimeExecutable, DiskInspection: inspector, Stdout: os.Stdout, Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	views := run.InstalledDiskInventories()
	if run.Status != lifecycle.RunComplete || run.Attempt != expectedRun.Attempt ||
		run.DefinitionDigest != expectedRun.DefinitionDigest || run.EnvironmentDigest != expectedRun.EnvironmentDigest ||
		run.PlanDigest != expectedRun.PlanDigest || len(views) != 1 || len(run.Observed) != 1 {
		t.Fatalf("real acceptance needs one completed disk and installed inventory: %+v", run)
	}
	view, output := views[0], run.Observed[0]
	if output.Disk == nil || output.Disk.Format != "qcow2" || output.Disk.Encryption != "none" ||
		view.OutputName != output.Name || view.Subject.PlanDigest != run.PlanDigest ||
		view.Subject.BuildDigest != output.BuildDigest || view.Subject.Digest != output.Digest ||
		view.Subject.Size != output.Disk.Size || len(view.Packages) == 0 {
		t.Fatalf("real inventory does not bind the original attempt output: %+v", view)
	}
	raw, err := canonical.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real live %s inspection, attempt=%s Definition=%s Environment=%s Plan=%s inventory=%s",
		runtime.GOARCH, run.Attempt, run.DefinitionDigest, run.EnvironmentDigest, run.PlanDigest, raw)
}

func requiredLiveInspectionValue(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("real live inspection requires explicit %s", name)
	}
	return value
}

func requiredLiveInspectionNumber(t *testing.T, name string, bits int) uint64 {
	t.Helper()
	value, err := strconv.ParseUint(requiredLiveInspectionValue(t, name), 10, bits)
	if err != nil || value == 0 {
		t.Fatalf("real live inspection requires positive %s: %v", name, err)
	}
	return value
}
