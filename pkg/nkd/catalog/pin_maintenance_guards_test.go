package catalog

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/releaseasset"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPinRuntimeRejectsNondefaultBeforeInputWork(t *testing.T) {
	for _, runtime := range []string{"podman", "nonexistent-legacy-runtime"} {
		t.Run(runtime, func(t *testing.T) {
			_, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: "must-not-be-read", Runtime: runtime})
			assertCatalogContractCode(t, err, e.CodeUsage)
			_, err = UpdateArtifactPins(t.Context(), ArtifactPinRequest{Directory: "must-not-be-read", Runtime: runtime})
			assertCatalogContractCode(t, err, e.CodeUsage)
			pins, err := resolveImageDigests(t.Context(), runtime, "must-not-be-contacted.invalid/tool:v1", []string{"amd64"})
			assertCatalogContractCode(t, err, e.CodeUsage)
			if pins != nil {
				t.Fatal("invalid runtime returned pins")
			}
		})
	}
	for _, runtime := range []string{"", "docker"} {
		if err := validatePinRuntime(runtime); err != nil {
			t.Fatalf("legacy default rejected: %v", err)
		}
	}
}

func TestResolveSourceRevisionRejectsFetchedCommitMismatch(t *testing.T) {
	commit := strings.Repeat("a", 40)
	var calls []string
	pin, err := resolveSourceRevisionWithGit(t.Context(), Source{Repo: "https://example.invalid/source"}, commit, commit,
		func(_ context.Context, _ string, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			if args[0] == "rev-parse" {
				return strings.Repeat("b", 40) + "\n", nil
			}
			return "", nil
		})
	assertCatalogContractCode(t, err, e.CodeFetchFailed)
	if !reflect.DeepEqual(pin, Pin{}) || len(calls) != 4 || calls[3] != "rev-parse FETCH_HEAD^{commit}" {
		t.Fatalf("substituted fetch produced a pin or later tree work: pin=%+v calls=%v", pin, calls)
	}
}

func TestToolArchiveLimits(t *testing.T) {
	want := releaseasset.Limits{ExecutableBytes: 256 << 20, ExpandedBytes: 1 << 30, Entries: 4096}
	if got := toolArchiveLimits(); got != want || maxToolArchiveBytes != 256<<20 {
		t.Fatalf("tool pin acquisition bounds changed: %+v", got)
	}
}

func TestReleaseExecutableMaintenanceDoesNotCreateArtifactPin(t *testing.T) {
	fixture := newPinSetIntegrationFixture(t, "")
	source := fixture.registry.Sources["cwe"]
	source.Tool = &ToolSource{Format: "raw", URL: "https://example.invalid/{{.Version}}/tool", Platform: "linux/amd64", ExecutablePath: "tool"}
	fixture.registry.Sources["cwe"] = source
	fixture.registry.Components["controller"] = rawComponent{
		Source: "cwe", Platforms: []string{"linux/amd64"}, OutputType: "host-package",
		Prebuilt: &Prebuilt{ReleaseExecutable: true, Evidence: &ExternalEvidence{None: true}},
	}
	fixture.writeRegistry(t)
	update, err := updateSourcePinsWithArtifacts(t.Context(), SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: "1.0.0"}, nil,
		func(ctx context.Context, src Source, tag string) (Pin, error) {
			tool := src.Tool
			src.Tool = nil
			pin, err := ResolvePin(ctx, src, tag)
			if err != nil || tool == nil {
				return pin, err
			}
			location, err := renderRef("fixture tool", tool.URL, pin)
			if err != nil {
				return Pin{}, err
			}
			pin.Tool = &ToolPin{Format: tool.Format, URL: location, Platform: tool.Platform, ExecutablePath: tool.ExecutablePath,
				ArchiveDigest: goodArtifactDigest, ExecutableDigest: goodArtifactDigest}
			return pin, nil
		}, func(context.Context, PrebuiltTarget, string) (ArtifactPin, error) {
			t.Fatal("releaseExecutable must not use component artifact maintenance")
			return ArtifactPin{}, nil
		})
	if err != nil || len(update.Pins) != 2 || len(update.Artifacts) != 0 {
		t.Fatalf("source tool update=%+v error=%v", update, err)
	}
	lock := fixture.lock(t)
	if len(lock.Artifacts) != 0 || lock.Pins["cwe"].Tool == nil {
		t.Fatal("tool pins did not remain solely source-owned")
	}
	targets, err := prebuiltTargetsFrom(fixture.registry, lock)
	if err != nil || len(targets) != 0 {
		t.Fatalf("releaseExecutable appeared in artifact selection: %v, %v", targets, err)
	}
}
