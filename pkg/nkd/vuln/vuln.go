// Package vuln authenticates subject-bound vulnerability reports for release gates.
// Scanner execution and advisory generation are outside the current product scope.
package vuln

import "github.com/nhensley/nkdctl/pkg/nkd/canonical"

const (
	QualifiedReportSchemaVersion = "nkd.dev/vuln-report/v2"
	ScannerGrype                 = "grype"
	ScannerTrivy                 = "trivy"
)

// Finding is one vulnerability match (scanner-agnostic, deterministic subset).
type Finding struct {
	ID       string `json:"id"`       // e.g. CVE-2025-1234
	Package  string `json:"package"`  // affected package
	Version  string `json:"version"`  // affected version
	Severity string `json:"severity"` // critical|high|medium|low|unknown
	FixedIn  string `json:"fixedIn,omitempty"`
}

// Report is the deterministic vuln report: it names the scanned artifact, the pinned
// DB/policy version, and the recipe identity — scan-time OMITTED. Findings are sorted
// so two runs over the same SBOM + pinned DB are byte-identical.
type Report struct {
	SchemaVersion  string    `json:"schemaVersion"`
	Scanner        string    `json:"scanner"`
	ScannerDigest  string    `json:"scannerDigest,omitempty"`
	DBVersion      string    `json:"dbVersion"`
	DatabaseDigest string    `json:"databaseDigest,omitempty"`
	RecipeDigest   string    `json:"recipeDigest"`
	Artifact       string    `json:"artifact"`
	SBOMDigest     string    `json:"sbomDigest,omitempty"`
	ResultDigest   string    `json:"resultDigest,omitempty"`
	Findings       []Finding `json:"findings"`
}

// Digest is the report's canonical content digest.
func (r *Report) Digest() (string, error) { return canonical.Digest(canonical.KindVulnReport, r) }

// Marshal renders the report in the canonical JCS codec (file bytes == hashed bytes).
func (r *Report) Marshal() ([]byte, error) { return canonical.Marshal(r) }
