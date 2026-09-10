package vuln

import (
	"encoding/json"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestTrivyQualificationNormalizesAndBindsSerializedFindings(t *testing.T) {
	request, scanner := qualifiedFixture(t)
	request.Scanner = ScannerTrivy
	scanner.observation.Result = []byte(`{"Results":[{"Vulnerabilities":[{"VulnerabilityID":" CVE-1 ","PkgName":" z ","InstalledVersion":" 2 ","Severity":" CRITICAL ","FixedVersion":" 3 "},{"VulnerabilityID":"CVE-1","PkgName":"a","InstalledVersion":"2","Severity":"UNKNOWN"},{"VulnerabilityID":"CVE-1","PkgName":"a","InstalledVersion":"1","Severity":"NEGLIGIBLE"}]}]}`)
	qualified, err := QualifyScan(t.Context(), request, scanner)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := qualified.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	want := []Finding{{ID: "CVE-1", Package: "a", Version: "1", Severity: "negligible"}, {ID: "CVE-1", Package: "a", Version: "2", Severity: "unknown"}, {ID: "CVE-1", Package: "z", Version: "2", Severity: "critical", FixedIn: "3"}}
	if !reflect.DeepEqual(report.Findings, want) || report.ResultDigest != rawSHA256(scanner.observation.Result) {
		t.Fatalf("lost scan bindings: %+v", report)
	}
	digest, err := report.Digest()
	if err != nil {
		t.Fatal(err)
	}
	qualifiedDigest, err := qualified.Digest()
	if err != nil || digest != qualifiedDigest {
		t.Fatalf("serialized identity changed: %s, %v", qualifiedDigest, err)
	}
	if _, err := (QualifiedReport{}).Marshal(); code(err) != e.CodeUnverifiedRef {
		t.Fatalf("forged report serialized: %v", err)
	}
}

func TestScannerResultSchemasFailClosed(t *testing.T) {
	for _, tc := range []struct{ name, scanner, raw string }{
		{"grype scalar", ScannerGrype, `42`},
		{"grype null matches", ScannerGrype, `{"matches":null}`},
		{"grype wrong matches", ScannerGrype, `{"matches":{}}`},
		{"trivy scalar", ScannerTrivy, `true`},
		{"trivy missing results", ScannerTrivy, `{}`},
		{"trivy null results", ScannerTrivy, `{"Results":null}`},
		{"trivy wrong results", ScannerTrivy, `{"Results":{}}`},
		{"trivy missing findings", ScannerTrivy, `{"Results":[{}]}`},
		{"trivy null findings", ScannerTrivy, `{"Results":[{"Vulnerabilities":null}]}`},
		{"trivy wrong findings", ScannerTrivy, `{"Results":[{"Vulnerabilities":{}}]}`},
		{"trivy invalid severity", ScannerTrivy, `{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-1","PkgName":"a","InstalledVersion":"1","Severity":"safe"}]}]}`},
		{"duplicate nested name", ScannerTrivy, `{"Results":[{"Vulnerabilities":[],"Vulnerabilities":[]}]}`},
		{"multiple documents", ScannerTrivy, `{"Results":[]} {}`},
		{"trailing garbage", ScannerTrivy, `{"Results":[]} !`},
		{"truncated array", ScannerTrivy, `{"Results":[`},
		{"truncated object", ScannerTrivy, `{"Results":[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, scanner := qualifiedFixture(t)
			request.Scanner = tc.scanner
			scanner.observation.Result = []byte(tc.raw)
			qualified, err := QualifyScan(t.Context(), request, scanner)
			if code(err) != e.CodeVerifyFailed {
				t.Fatalf("invalid result accepted: %v", err)
			}
			if _, err := qualified.Report(); code(err) != e.CodeUnverifiedRef {
				t.Fatalf("failure issued report: %v", err)
			}
		})
	}
}
