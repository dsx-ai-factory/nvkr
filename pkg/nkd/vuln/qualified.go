package vuln

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// QualifiedScanRequest binds a release scan to exact subject, SBOM, scanner, and
// database identities. Scanner/DB pins are content identities, not mutable names.
type QualifiedScanRequest struct {
	Scanner        string
	ScannerDigest  string
	DatabaseDigest string
	RecipeDigest   string
	ArtifactDigest string
	SBOMDigest     string
	CanonicalSBOM  []byte
}

// ScanObservation is returned by the scanner adapter. QualifyScan checks both
// observed execution identities against the request and computes ResultDigest from
// Result bytes; an adapter never supplies a passing boolean.
type ScanObservation struct {
	ScannerDigest  string
	DatabaseDigest string
	Result         []byte
}

// ReleaseScanner is the context-aware subprocess/container seam. Implementations
// are responsible for running with network disabled and reporting the identities of
// the scanner and DB material actually used.
type ReleaseScanner interface {
	Scan(ctx context.Context, request QualifiedScanRequest) (ScanObservation, error)
}

// QualifiedReport is the release evidence capability. Its critical state is
// private and can only be created by QualifyScan after all identities and the raw
// result schema have been verified.
type QualifiedReport struct {
	report *Report
	valid  bool
}

// QualifyScan fails closed if the scanner adapter, scanner identity, database pin,
// SBOM, or result is absent. It parses supported scanner output strictly: malformed
// or unrecognized output cannot become a zero-finding report.
func QualifyScan(ctx context.Context, request QualifiedScanRequest, scanner ReleaseScanner) (QualifiedReport, error) {
	if ctx == nil {
		return QualifiedReport{}, e.New(e.CodeUsage, "qualifying vulnerability scan requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return QualifiedReport{}, e.Wrap(e.CodeTimeout, err, "qualifying vulnerability scan")
	}
	if scanner == nil || strings.TrimSpace(request.Scanner) == "" {
		return QualifiedReport{}, e.New(e.CodeUnsupported, "release-qualified vulnerability evidence requires a scanner")
	}
	if request.Scanner != ScannerGrype && request.Scanner != ScannerTrivy {
		return QualifiedReport{}, e.New(e.CodeUnsupported, "vuln: unknown scanner %q (grype|trivy)", request.Scanner)
	}
	for _, identity := range []struct{ name, digest string }{
		{"scanner", request.ScannerDigest}, {"database", request.DatabaseDigest},
		{"recipe", request.RecipeDigest}, {"artifact", request.ArtifactDigest}, {"SBOM", request.SBOMDigest},
	} {
		if !canonical.IsDigest(identity.digest) {
			return QualifiedReport{}, e.New(e.CodeUnpinnedInput,
				"release-qualified vulnerability %s identity is not pinned by sha256:<64-hex>: %q", identity.name, identity.digest)
		}
	}
	if len(bytes.TrimSpace(request.CanonicalSBOM)) == 0 {
		return QualifiedReport{}, e.New(e.CodeProvenanceMissing, "release-qualified vulnerability scan has no SBOM bytes")
	}
	sbomDigest, err := canonicalJSONDigest(canonical.KindSBOM, request.CanonicalSBOM)
	if err != nil {
		return QualifiedReport{}, err
	}
	if sbomDigest != request.SBOMDigest {
		return QualifiedReport{}, e.New(e.CodeUnverifiedRef,
			"vulnerability scan SBOM hashes to %s, not pinned %s", sbomDigest, request.SBOMDigest)
	}

	// Give the adapter a private copy and verify it remains unchanged. The scanner
	// must observe the exact canonical SBOM whose digest was checked above; a mutable
	// caller slice or adapter must not switch bytes between qualification and scan.
	scannerRequest := request
	scannerRequest.CanonicalSBOM = append([]byte(nil), request.CanonicalSBOM...)
	scannedSBOM := append([]byte(nil), scannerRequest.CanonicalSBOM...)
	observation, err := scanner.Scan(ctx, scannerRequest)
	if err != nil {
		if ctx.Err() != nil {
			return QualifiedReport{}, e.Wrap(e.CodeTimeout, ctx.Err(), "running release-qualified vulnerability scan")
		}
		return QualifiedReport{}, e.Wrap(e.CodeVerifyFailed, err, "running release-qualified vulnerability scan")
	}
	if !bytes.Equal(scannerRequest.CanonicalSBOM, scannedSBOM) {
		return QualifiedReport{}, e.New(e.CodeVerifyFailed,
			"scanner adapter changed the canonical SBOM bytes during qualification")
	}
	if observation.ScannerDigest == "" || observation.DatabaseDigest == "" {
		return QualifiedReport{}, e.New(e.CodeUnpinnedInput,
			"scanner adapter omitted the observed scanner or database identity")
	}
	if observation.ScannerDigest != request.ScannerDigest {
		return QualifiedReport{}, e.New(e.CodeUnverifiedRef,
			"scanner observation %s does not match pinned %s", observation.ScannerDigest, request.ScannerDigest)
	}
	if observation.DatabaseDigest != request.DatabaseDigest {
		return QualifiedReport{}, e.New(e.CodeUnverifiedRef,
			"scanner database observation %s does not match pinned %s", observation.DatabaseDigest, request.DatabaseDigest)
	}
	if len(bytes.TrimSpace(observation.Result)) == 0 {
		return QualifiedReport{}, e.New(e.CodeVerifyFailed, "scanner returned no result bytes")
	}
	findings, err := parseQualifiedResult(request.Scanner, observation.Result)
	if err != nil {
		return QualifiedReport{}, err
	}
	resultDigest := rawSHA256(observation.Result)
	report := &Report{
		SchemaVersion: QualifiedReportSchemaVersion,
		Scanner:       request.Scanner, ScannerDigest: request.ScannerDigest,
		DBVersion: request.DatabaseDigest, DatabaseDigest: request.DatabaseDigest,
		RecipeDigest: request.RecipeDigest, Artifact: request.ArtifactDigest,
		SBOMDigest: request.SBOMDigest, ResultDigest: resultDigest, Findings: findings,
	}
	return QualifiedReport{report: report, valid: true}, nil
}

// Report returns a defensive copy of the verified report.
func (qualified QualifiedReport) Report() (*Report, error) {
	if !qualified.valid || qualified.report == nil {
		return nil, e.New(e.CodeUnverifiedRef, "vulnerability report was not produced by a qualified scan")
	}
	copy := *qualified.report
	copy.Findings = append([]Finding(nil), qualified.report.Findings...)
	return &copy, nil
}

// Digest returns the canonical identity of the verified report.
func (qualified QualifiedReport) Digest() (string, error) {
	report, err := qualified.Report()
	if err != nil {
		return "", err
	}
	return report.Digest()
}

// Marshal returns canonical verified report bytes.
func (qualified QualifiedReport) Marshal() ([]byte, error) {
	report, err := qualified.Report()
	if err != nil {
		return nil, err
	}
	return report.Marshal()
}

func parseQualifiedResult(scanner string, raw []byte) ([]Finding, error) {
	if err := rejectDuplicateResultNames(raw); err != nil {
		return nil, err
	}
	var findings []Finding
	switch scanner {
	case ScannerGrype:
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding grype result")
		}
		matches, ok := envelope["matches"]
		if !ok || bytes.Equal(bytes.TrimSpace(matches), []byte("null")) {
			return nil, e.New(e.CodeVerifyFailed, "grype result omits the matches array")
		}
		var parsed []struct {
			Vulnerability struct {
				ID       string `json:"id"`
				Severity string `json:"severity"`
				Fix      struct {
					Versions []string `json:"versions"`
				} `json:"fix"`
			} `json:"vulnerability"`
			Artifact struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"artifact"`
		}
		if err := json.Unmarshal(matches, &parsed); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding grype matches")
		}
		for _, match := range parsed {
			fixed := ""
			if len(match.Vulnerability.Fix.Versions) > 0 {
				fixed = match.Vulnerability.Fix.Versions[0]
			}
			findings = append(findings, Finding{ID: match.Vulnerability.ID, Package: match.Artifact.Name,
				Version: match.Artifact.Version, Severity: match.Vulnerability.Severity, FixedIn: fixed})
		}
	case ScannerTrivy:
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding trivy result")
		}
		results, ok := envelope["Results"]
		if !ok || bytes.Equal(bytes.TrimSpace(results), []byte("null")) {
			return nil, e.New(e.CodeVerifyFailed, "trivy result omits the Results array")
		}
		var parsed []map[string]json.RawMessage
		if err := json.Unmarshal(results, &parsed); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding trivy Results")
		}
		for index, result := range parsed {
			rawVulnerabilities, ok := result["Vulnerabilities"]
			if !ok || bytes.Equal(bytes.TrimSpace(rawVulnerabilities), []byte("null")) {
				return nil, e.New(e.CodeVerifyFailed, "trivy result %d omits the Vulnerabilities array", index)
			}
			var vulnerabilities []struct {
				ID       string `json:"VulnerabilityID"`
				Package  string `json:"PkgName"`
				Version  string `json:"InstalledVersion"`
				Severity string `json:"Severity"`
				FixedIn  string `json:"FixedVersion"`
			}
			if err := json.Unmarshal(rawVulnerabilities, &vulnerabilities); err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding trivy vulnerabilities")
			}
			for _, vulnerability := range vulnerabilities {
				findings = append(findings, Finding{ID: vulnerability.ID, Package: vulnerability.Package,
					Version: vulnerability.Version, Severity: vulnerability.Severity, FixedIn: vulnerability.FixedIn})
			}
		}
	default:
		return nil, e.New(e.CodeUnsupported, "vuln: unknown scanner %q", scanner)
	}
	for index := range findings {
		finding := &findings[index]
		finding.ID = strings.TrimSpace(finding.ID)
		finding.Package = strings.TrimSpace(finding.Package)
		finding.Version = strings.TrimSpace(finding.Version)
		finding.Severity = strings.ToLower(strings.TrimSpace(finding.Severity))
		finding.FixedIn = strings.TrimSpace(finding.FixedIn)
		if finding.ID == "" || finding.Package == "" || finding.Version == "" || !validSeverity(finding.Severity) {
			return nil, e.New(e.CodeVerifyFailed, "scanner result contains an incomplete or invalid finding: %+v", finding)
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].ID != findings[j].ID {
			return findings[i].ID < findings[j].ID
		}
		if findings[i].Package != findings[j].Package {
			return findings[i].Package < findings[j].Package
		}
		return findings[i].Version < findings[j].Version
	})
	for index := 1; index < len(findings); index++ {
		previous, current := findings[index-1], findings[index]
		if previous.ID == current.ID && previous.Package == current.Package && previous.Version == current.Version {
			return nil, e.New(e.CodeVerifyFailed,
				"scanner result repeats finding %s for %s@%s", current.ID, current.Package, current.Version)
		}
	}
	return findings, nil
}

func rejectDuplicateResultNames(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkResultJSON(decoder); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err,
			"validating scanner result JSON member names")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return e.New(e.CodeVerifyFailed, "scanner result contains more than one JSON document")
		}
		return e.Wrap(e.CodeVerifyFailed, err, "checking scanner result JSON boundary")
	}
	return nil
}

func walkResultJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return e.New(e.CodeVerifyFailed, "scanner result object member name is not a string")
			}
			if seen[name] {
				return e.New(e.CodeVerifyFailed, "scanner result repeats JSON member %q", name)
			}
			seen[name] = true
			if err := walkResultJSON(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return e.New(e.CodeVerifyFailed, "malformed scanner result object")
		}
	case '[':
		for decoder.More() {
			if err := walkResultJSON(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return e.New(e.CodeVerifyFailed, "malformed scanner result array")
		}
	default:
		return e.New(e.CodeVerifyFailed, "unexpected scanner result JSON delimiter %q", delimiter)
	}
	return nil
}

func validSeverity(severity string) bool {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high", "medium", "low", "negligible", "unknown":
		return true
	default:
		return false
	}
}

func canonicalJSONDigest(kind string, raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "decoding canonical evidence bytes")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", e.New(e.CodeVerifyFailed, "evidence contains more than one JSON document")
		}
		return "", e.Wrap(e.CodeVerifyFailed, err, "decoding trailing evidence bytes")
	}
	canonicalBytes, err := canonical.Marshal(document)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(raw, canonicalBytes) {
		return "", e.New(e.CodeVerifyFailed, "evidence bytes are not in the canonical representation bound by their digest")
	}
	return canonical.Digest(kind, document)
}

func rawSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
