package vuln

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

type releaseScanner struct {
	observation ScanObservation
	err         error
}

type mutatingScanner struct{ observation ScanObservation }

func (scanner mutatingScanner) Scan(_ context.Context, request QualifiedScanRequest) (ScanObservation, error) {
	request.CanonicalSBOM[0] ^= 0xff
	return scanner.observation, nil
}

func (scanner releaseScanner) Scan(context.Context, QualifiedScanRequest) (ScanObservation, error) {
	return scanner.observation, scanner.err
}

func vulnExact(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func qualifiedFixture(t *testing.T) (QualifiedScanRequest, releaseScanner) {
	t.Helper()
	sbomDocument := map[string]any{
		"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1,
		"components": []any{},
	}
	sbomBytes, err := canonical.Marshal(sbomDocument)
	if err != nil {
		t.Fatal(err)
	}
	sbomDigest, err := canonical.Digest(canonical.KindSBOM, sbomDocument)
	if err != nil {
		t.Fatal(err)
	}
	request := QualifiedScanRequest{
		Scanner: ScannerGrype, ScannerDigest: vulnExact("grype-binary"),
		DatabaseDigest: vulnExact("grype-db"), RecipeDigest: vulnExact("recipe"),
		ArtifactDigest: vulnExact("artifact"), SBOMDigest: sbomDigest, CanonicalSBOM: sbomBytes,
	}
	result := []byte(`{"matches":[{"vulnerability":{"id":"CVE-2","severity":"High","fix":{"versions":["2.0"]}},"artifact":{"name":"pkg-b","version":"1.0"}},{"vulnerability":{"id":"CVE-1","severity":"Low"},"artifact":{"name":"pkg-a","version":"1.1"}}]}`)
	scanner := releaseScanner{observation: ScanObservation{
		ScannerDigest: request.ScannerDigest, DatabaseDigest: request.DatabaseDigest, Result: result,
	}}
	return request, scanner
}

func TestQualifyScanBindsAllExactIdentities(t *testing.T) {
	request, scanner := qualifiedFixture(t)
	qualified, err := QualifyScan(context.Background(), request, scanner)
	if err != nil {
		t.Fatal(err)
	}
	report, err := qualified.Report()
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != QualifiedReportSchemaVersion || report.ScannerDigest != request.ScannerDigest ||
		report.DatabaseDigest != request.DatabaseDigest || report.Artifact != request.ArtifactDigest ||
		report.SBOMDigest != request.SBOMDigest || report.ResultDigest != rawSHA256(scanner.observation.Result) {
		t.Fatalf("qualified report lost an exact identity binding: %+v", report)
	}
	if len(report.Findings) != 2 || report.Findings[0].ID != "CVE-1" || report.Findings[0].Severity != "low" {
		t.Fatalf("findings were not strictly parsed and sorted: %+v", report.Findings)
	}
	digest1, err := qualified.Digest()
	if err != nil {
		t.Fatal(err)
	}
	qualified2, err := QualifyScan(context.Background(), request, scanner)
	if err != nil {
		t.Fatal(err)
	}
	digest2, _ := qualified2.Digest()
	if digest1 != digest2 {
		t.Fatalf("same qualified inputs produced different digests: %s vs %s", digest1, digest2)
	}
}

func TestQualifyScanFailsClosedOnAbsentOrUnpinnedInputs(t *testing.T) {
	request, scanner := qualifiedFixture(t)
	for _, test := range []struct {
		name string
		edit func(*QualifiedScanRequest, *releaseScanner)
		want nkderr.Code
	}{
		{"scanner pin", func(req *QualifiedScanRequest, _ *releaseScanner) { req.ScannerDigest = "" }, nkderr.CodeUnpinnedInput},
		{"database pin", func(req *QualifiedScanRequest, _ *releaseScanner) { req.DatabaseDigest = "db-latest" }, nkderr.CodeUnpinnedInput},
		{"SBOM bytes", func(req *QualifiedScanRequest, _ *releaseScanner) { req.CanonicalSBOM = nil }, nkderr.CodeProvenanceMissing},
		{"observed scanner", func(_ *QualifiedScanRequest, scanner *releaseScanner) { scanner.observation.ScannerDigest = "" }, nkderr.CodeUnpinnedInput},
		{"observed database", func(_ *QualifiedScanRequest, scanner *releaseScanner) { scanner.observation.DatabaseDigest = "" }, nkderr.CodeUnpinnedInput},
		{"result bytes", func(_ *QualifiedScanRequest, scanner *releaseScanner) { scanner.observation.Result = nil }, nkderr.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			reqCopy := request
			reqCopy.CanonicalSBOM = append([]byte(nil), request.CanonicalSBOM...)
			scannerCopy := scanner
			scannerCopy.observation.Result = append([]byte(nil), scanner.observation.Result...)
			test.edit(&reqCopy, &scannerCopy)
			if _, err := QualifyScan(context.Background(), reqCopy, scannerCopy); code(err) != test.want {
				t.Fatalf("want %s, got %v", test.want, err)
			}
		})
	}
	if _, err := QualifyScan(context.Background(), request, nil); code(err) != nkderr.CodeUnsupported {
		t.Fatalf("missing scanner seam must fail closed, got %v", err)
	}
}

func TestQualifyScanRejectsMismatchesAndMalformedResults(t *testing.T) {
	request, scanner := qualifiedFixture(t)
	tests := []struct {
		name   string
		mutate func(*QualifiedScanRequest, *releaseScanner)
		want   nkderr.Code
	}{
		{"SBOM mismatch", func(req *QualifiedScanRequest, _ *releaseScanner) {
			req.CanonicalSBOM = []byte(`{"bomFormat":"tampered"}`)
		}, nkderr.CodeUnverifiedRef},
		{"scanner mismatch", func(_ *QualifiedScanRequest, scan *releaseScanner) {
			scan.observation.ScannerDigest = vulnExact("other-scanner")
		}, nkderr.CodeUnverifiedRef},
		{"database mismatch", func(_ *QualifiedScanRequest, scan *releaseScanner) {
			scan.observation.DatabaseDigest = vulnExact("other-db")
		}, nkderr.CodeUnverifiedRef},
		{"malformed JSON", func(_ *QualifiedScanRequest, scan *releaseScanner) { scan.observation.Result = []byte(`not-json`) }, nkderr.CodeVerifyFailed},
		{"unknown shape", func(_ *QualifiedScanRequest, scan *releaseScanner) { scan.observation.Result = []byte(`{}`) }, nkderr.CodeVerifyFailed},
		{"duplicate result member", func(_ *QualifiedScanRequest, scan *releaseScanner) {
			scan.observation.Result = []byte(`{"matches":[],"matches":[]}`)
		}, nkderr.CodeVerifyFailed},
		{"duplicate finding", func(_ *QualifiedScanRequest, scan *releaseScanner) {
			scan.observation.Result = []byte(`{"matches":[{"vulnerability":{"id":"CVE-1","severity":"High"},"artifact":{"name":"pkg","version":"1"}},{"vulnerability":{"id":"CVE-1","severity":"high"},"artifact":{"name":"pkg","version":"1"}}]}`)
		}, nkderr.CodeVerifyFailed},
		{"incomplete finding", func(_ *QualifiedScanRequest, scan *releaseScanner) {
			scan.observation.Result = []byte(`{"matches":[{"vulnerability":{"id":"CVE-1","severity":"High"},"artifact":{}}]}`)
		}, nkderr.CodeVerifyFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reqCopy := request
			reqCopy.CanonicalSBOM = append([]byte(nil), request.CanonicalSBOM...)
			scannerCopy := scanner
			scannerCopy.observation.Result = append([]byte(nil), scanner.observation.Result...)
			test.mutate(&reqCopy, &scannerCopy)
			if _, err := QualifyScan(context.Background(), reqCopy, scannerCopy); code(err) != test.want {
				t.Fatalf("want %s, got %v", test.want, err)
			}
		})
	}
}

func TestQualifyScanRejectsAdapterMutationOfSBOMSnapshot(t *testing.T) {
	request, scanner := qualifiedFixture(t)
	adapter := mutatingScanner{observation: scanner.observation}
	if _, err := QualifyScan(context.Background(), request, adapter); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("scanner mutation of qualified SBOM bytes must fail closed: %v", err)
	}
}

func TestExplicitEmptyFindingsDifferFromMissingResult(t *testing.T) {
	request, scanner := qualifiedFixture(t)
	scanner.observation.Result = []byte(`{"matches":[]}`)
	qualified, err := QualifyScan(context.Background(), request, scanner)
	if err != nil {
		t.Fatalf("explicit scanner result with no findings should qualify: %v", err)
	}
	report, _ := qualified.Report()
	if len(report.Findings) != 0 || report.ResultDigest == "" {
		t.Fatalf("explicit empty result lost its byte identity: %+v", report)
	}
	scanner.observation.Result = []byte(`{}`)
	if _, err := QualifyScan(context.Background(), request, scanner); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("missing result shape must not masquerade as clean scan, got %v", err)
	}
}

func TestQualifyScanPropagatesExecutionFailureAndCancellation(t *testing.T) {
	request, scanner := qualifiedFixture(t)
	//nolint:staticcheck // Deliberately exercise the public nil-context contract.
	if _, err := QualifyScan(nil, request, scanner); code(err) != nkderr.CodeUsage {
		t.Fatalf("nil scan context must return usage, got %v", err)
	}
	scanner.err = errors.New("scanner unavailable")
	if _, err := QualifyScan(context.Background(), request, scanner); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("scanner failure must fail closed, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := QualifyScan(ctx, request, scanner); code(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled scan must return timeout, got %v", err)
	}
	if _, err := (QualifiedReport{}).Digest(); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("zero-value qualified report must not authorize, got %v", err)
	}
}
