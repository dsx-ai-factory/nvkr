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
