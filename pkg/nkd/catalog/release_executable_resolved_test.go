package catalog

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestReleaseExecutableResolvedClosureConsistency(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Component)
		code   e.Code
	}{
		{name: "exact closure"},
		{name: "unresolved declaration", change: func(c *Component) { c.Prebuilt.ReleaseExecutable = true }, code: e.CodeInvalidCatalog},
		{name: "wrong output", change: func(c *Component) { c.OutputType = "raw" }, code: e.CodeInvalidCatalog},
		{name: "multiple platforms", change: func(c *Component) { c.Platforms = append(c.Platforms, "linux/arm64") }, code: e.CodeInvalidCatalog},
		{name: "substituted platform", change: func(c *Component) { c.Platforms[0] = "linux/arm64" }, code: e.CodeInvalidCatalog},
		{name: "substituted URL", change: func(c *Component) { c.Prebuilt.URL = "https://example.invalid/other.tar.gz" }, code: e.CodeInvalidCatalog},
		{name: "extra digest", change: func(c *Component) { c.Prebuilt.Digests["arm64"] = c.Prebuilt.Executable.ExecutableDigest }, code: e.CodeInvalidCatalog},
		{name: "substituted member digest", change: func(c *Component) { c.Prebuilt.Digests["amd64"] = c.Prebuilt.Executable.ArchiveDigest }, code: e.CodeInvalidCatalog},
		{name: "missing archive pin", change: func(c *Component) { c.Prebuilt.Executable.ArchiveDigest = "" }, code: e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := ToolPin{Format: "tar-gzip", URL: "https://example.invalid/tool.tar.gz", Platform: "linux/amd64", ExecutablePath: "bin/tool", ArchiveDigest: goodArtifactDigest, ExecutableDigest: "sha256:" + strings.Repeat("b", 64)}
			component := Component{OutputType: "host-package", Platforms: []string{"linux/amd64"}, Prebuilt: &Prebuilt{URL: tool.URL, Digests: map[string]string{"amd64": tool.ExecutableDigest}, Executable: &tool, Evidence: &ExternalEvidence{None: true}}}
			if tc.change != nil {
				tc.change(&component)
			}
			if err := validatePrebuilt("tool", component); pinErrorCode(err) != tc.code {
				t.Fatalf("resolved closure: %v, want %s", err, tc.code)
			}
		})
	}
}

func TestReleaseExecutableRawDeclarationConsistency(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*rawComponent, *Source)
	}{
		{name: "source built", change: func(c *rawComponent, _ *Source) { c.FromSource = true }},
		{name: "independent pinnedBy", change: func(c *rawComponent, _ *Source) { c.PinnedBy = "upstream" }},
		{name: "competing image", change: func(c *rawComponent, _ *Source) { c.Prebuilt.Image = "example.invalid/tool:v1" }},
		{name: "competing chart", change: func(c *rawComponent, _ *Source) { c.Prebuilt.Chart = &HelmChart{} }},
		{name: "authored digests", change: func(c *rawComponent, _ *Source) { c.Prebuilt.Digests = map[string]string{"amd64": goodArtifactDigest} }},
		{name: "multiple platforms", change: func(c *rawComponent, _ *Source) { c.Platforms = append(c.Platforms, "linux/arm64") }},
		{name: "wrong output", change: func(c *rawComponent, _ *Source) { c.OutputType = "raw" }},
		{name: "resolved executable authored", change: func(c *rawComponent, _ *Source) { c.Prebuilt.Executable = &ToolPin{} }},
		{name: "missing tool", change: func(_ *rawComponent, s *Source) { s.Tool = nil }},
		{name: "unknown source", change: func(c *rawComponent, _ *Source) { c.Source = "absent" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := ToolSource{Format: "tar-gzip", URL: "https://example.invalid/tool.tar.gz", Platform: "linux/amd64", ExecutablePath: "bin/tool"}
			source := Source{Repo: "https://example.invalid/upstream", Tool: &tool}
			component := rawComponent{Source: "upstream", OutputType: "host-package", Platforms: []string{"linux/amd64"}, Prebuilt: &Prebuilt{ReleaseExecutable: true, Evidence: &ExternalEvidence{None: true}}}
			tc.change(&component, &source)
			registry := &registryDoc{Sources: map[string]Source{"upstream": source}, Components: map[string]rawComponent{"tool": component}}
			assertCatalogContractCode(t, validatePinSets(registry), e.CodeInvalidCatalog)
		})
	}
}

func TestReleaseExecutableSourceToolResolutionRequiresPin(t *testing.T) {
	for _, tc := range []struct {
		name             string
		declared, pinned bool
		wrongURL         bool
		code             e.Code
	}{
		{name: "both absent", code: e.CodeUnpinnedSource},
		{name: "missing source tool pin", declared: true, code: e.CodeUnpinnedSource},
		{name: "orphan tool pin", pinned: true, code: e.CodeInvalidCatalog},
		{name: "tool differs from source", declared: true, pinned: true, wrongURL: true, code: e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := ToolSource{Format: "tar-gzip", URL: "https://example.invalid/tool.tar.gz", Platform: "linux/amd64", ExecutablePath: "bin/tool"}
			source := Source{Repo: "https://example.invalid/upstream"}
			pin := Pin{}
			if tc.declared {
				source.Tool = &tool
			}
			if tc.pinned {
				pin.Tool = &ToolPin{Format: tool.Format, URL: tool.URL, Platform: tool.Platform, ExecutablePath: tool.ExecutablePath, ArchiveDigest: goodArtifactDigest, ExecutableDigest: goodArtifactDigest}
			}
			if tc.wrongURL {
				pin.Tool.URL = "https://example.invalid/substituted.tar.gz"
			}
			raw := rawComponent{Prebuilt: &Prebuilt{ReleaseExecutable: true}}
			resolved, err := resolvePrebuilt("tool", raw, source, pin, &PinLock{}, Component{})
			assertCatalogContractCode(t, err, tc.code)
			if resolved.Prebuilt != nil {
				t.Fatal("failed tool resolution exposed executable acquisition")
			}
		})
	}
}
