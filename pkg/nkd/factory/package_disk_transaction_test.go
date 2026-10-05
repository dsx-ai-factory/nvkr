package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// These tiny synthetic transport/evidence bytes test publication mechanics,
// not a live reconciliation receipt, inspected disk or production release.
func TestReconciledDiskPackageTransaction(t *testing.T) {
	for _, scenario := range []struct {
		name string
		code e.Code
	}{
		{name: "success"},
		{name: "build failure", code: e.CodeVerifyFailed},
		{name: "evidence failure", code: e.CodePipelineDrift},
		{name: "carrier close failure", code: e.CodeImmutableViolation},
		{name: "canceled before commit", code: e.CodeTimeout},
		{name: "second destination race", code: e.CodeImmutableViolation},
		{name: "existing package", code: e.CodeImmutableViolation},
		{name: "existing evidence", code: e.CodeImmutableViolation},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			workspace := t.TempDir()
			output := filepath.Join(workspace, "packages", "disk")
			evidence := reconciledEvidenceFiles{directory: filepath.Join(workspace, "sboms", "disk"),
				files: map[string][]byte{"disk.cdx.json": []byte("synthetic complete document\n"), "index.json": []byte("synthetic index\n")}}
			payload := []byte("synthetic opaque disk transport")
			payloadHash := sha256.Sum256(payload)
			payloadDigest := "sha256:" + hex.EncodeToString(payloadHash[:])
			source := filepath.Join(workspace, "source")
			if err := os.WriteFile(source, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			preserved := ""
			switch scenario.name {
			case "existing package":
				preserved = output
			case "existing evidence":
				preserved = evidence.directory
			}
			writePreserved := func(path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte("pre-existing caller data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if preserved != "" {
				writePreserved(preserved)
			}
			if scenario.name == "evidence failure" {
				evidence.files["invalid/member"] = []byte("not a safe evidence slot")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			closeCarrier := func() error {
				for _, path := range []string{output, evidence.directory} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("carrier cleanup ran after public output: %s, %v", path, err)
					}
				}
				switch scenario.name {
				case "carrier close failure":
					return errors.New("injected carrier cleanup failure")
				case "canceled before commit":
					cancel()
				case "second destination race":
					preserved = evidence.directory
					writePreserved(preserved)
				}
				return nil
			}
			buildCalls := 0
			build := func(ctx context.Context, path string) (delivery.Result, error) {
				buildCalls++
				result, err := delivery.BuildDiskArtifact(ctx, path, delivery.DiskArtifactRequest{
					Path: source, Digest: payloadDigest, Size: int64(len(payload)), Platform: "linux/amd64",
					Format: "qcow2", Encryption: "none", ArtifactType: "application/vnd.example.disk.v1"})
				if err != nil {
					return delivery.Result{}, err
				}
				if scenario.name == "build failure" {
					return delivery.Result{}, e.New(e.CodeVerifyFailed, "injected build failure after private wrapper construction")
				}
				return result, nil
			}
			result, err := publishReconciledDiskPackage(ctx, workspace, output, evidence, closeCarrier, build)
			if factoryErrorCode(err) != scenario.code {
				t.Fatalf("got %v, want %s", err, scenario.code)
			}
			if scenario.code == "" {
				if result.LayoutPath != output || result.RootDigest == "" {
					t.Fatalf("wrong committed wrapper result: %+v", result)
				}
				for name, expected := range evidence.files {
					actual, err := os.ReadFile(filepath.Join(evidence.directory, name))
					if err != nil || !bytes.Equal(actual, expected) {
						t.Fatalf("committed evidence %s: %q, %v", name, actual, err)
					}
				}
			} else {
				if !reflect.DeepEqual(result, delivery.Result{}) {
					t.Fatalf("failure returned public identity: %+v", result)
				}
				for _, path := range []string{output, evidence.directory} {
					if path == preserved {
						actual, err := os.ReadFile(filepath.Join(path, "sentinel"))
						if err != nil || string(actual) != "pre-existing caller data" {
							t.Fatalf("pre-existing destination changed: %q, %v", actual, err)
						}
						continue
					}
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failure left public output %s: %v", path, err)
					}
				}
			}
			if strings.HasPrefix(scenario.name, "existing ") && buildCalls != 0 {
				t.Fatal("existing destination admission occurred after construction")
			}
			entries, err := os.ReadDir(workspace)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".nkd-download-") {
					t.Fatalf("transaction leaked private stage %s", entry.Name())
				}
			}
		})
	}
}
