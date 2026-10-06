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

	"github.com/NVIDIA/nke-recipes/internal/securepath"
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

func TestDiskPackageRollbackPreservesForeignRoots(t *testing.T) {
	for _, scenario := range []string{"success", "swapped root", "missing root", "closed parent", "occupied stage"} {
		t.Run(scenario, func(t *testing.T) {
			workspace := t.TempDir()
			published := filepath.Join(workspace, "published")
			private := filepath.Join(workspace, "private")
			for _, path := range []string{published, private} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(published, "sentinel"), []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			parent, _, err := securepath.OpenDirectory(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close() }()
			stage, _, err := securepath.OpenDirectory(private)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stage.Close() }()
			original, err := os.Stat(published)
			if err != nil {
				t.Fatal(err)
			}
			retained := published
			foreign := ""
			switch scenario {
			case "swapped root", "missing root":
				retained = filepath.Join(workspace, "retained")
				if err := os.Rename(published, retained); err != nil {
					t.Fatal(err)
				}
				if scenario == "swapped root" {
					foreign = published
				}
			case "closed parent":
				if err := parent.Close(); err != nil {
					t.Fatal(err)
				}
			case "occupied stage":
				foreign = filepath.Join(private, "package")
			}
			if foreign != "" {
				if err := os.Mkdir(foreign, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(foreign, "sentinel"), []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err = rollbackDiskPackageRoot(parent, "published", original, stage, "package")
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				retained = filepath.Join(private, "package")
				if _, err := os.Lstat(published); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rollback left public root: %v", err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeImmutableViolation)
			}
			for path, expected := range map[string]string{retained: "original", foreign: "foreign"} {
				if path == "" {
					continue
				}
				actual, err := os.ReadFile(filepath.Join(path, "sentinel"))
				if err != nil || string(actual) != expected {
					t.Fatalf("rollback changed %s: %q, %v", path, actual, err)
				}
			}
		})
	}
}

func TestDiskPackageCleanupRejectsChangedStage(t *testing.T) {
	for _, scenario := range []string{"missing stage", "swapped stage"} {
		t.Run(scenario, func(t *testing.T) {
			workspace := t.TempDir()
			retained := filepath.Join(t.TempDir(), "retained-stage")
			var stage string
			build := func(_ context.Context, path string) (delivery.Result, error) {
				stage = filepath.Dir(path)
				if err := os.Rename(stage, retained); err != nil {
					t.Fatal(err)
				}
				if scenario == "swapped stage" {
					if err := os.Mkdir(stage, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(stage, "sentinel"), []byte("foreign"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return delivery.Result{}, e.New(e.CodeVerifyFailed, "controlled construction failure")
			}
			result, err := publishReconciledDiskPackage(t.Context(), workspace, filepath.Join(workspace, "package"),
				reconciledEvidenceFiles{directory: filepath.Join(workspace, "evidence")},
				func() error { t.Fatal("cleanup callback reached after construction failure"); return nil }, build)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), "private disk package stage changed") || !reflect.DeepEqual(result, delivery.Result{}) {
				t.Fatalf("stage cleanup lost failure or exposed result: %+v, %v", result, err)
			}
			if scenario == "swapped stage" {
				actual, err := os.ReadFile(filepath.Join(stage, "sentinel"))
				if err != nil || string(actual) != "foreign" {
					t.Fatalf("cleanup touched foreign stage: %q, %v", actual, err)
				}
			}
			for _, name := range []string{"package", "evidence"} {
				if _, err := os.Lstat(filepath.Join(workspace, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed construction published %s: %v", name, err)
				}
			}
		})
	}
}

func TestReconciledDiskPackageTransactionAdmission(t *testing.T) {
	for _, scenario := range []struct {
		name string
		code e.Code
	}{
		{name: "missing context", code: e.CodeUsage},
		{name: "missing cleanup", code: e.CodeUsage},
		{name: "missing builder", code: e.CodeUsage},
		{name: "canceled", code: e.CodeTimeout},
		{name: "package escape", code: e.CodePipelineDrift},
		{name: "evidence escape", code: e.CodePipelineDrift},
		{name: "same destination", code: e.CodePipelineDrift},
		{name: "package contains evidence", code: e.CodePipelineDrift},
		{name: "evidence contains package", code: e.CodePipelineDrift},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			workspace := t.TempDir()
			foreign := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(foreign, []byte("caller-owned"), 0o600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(workspace, "package")
			evidence := reconciledEvidenceFiles{directory: filepath.Join(workspace, "evidence")}
			ctx := t.Context()
			calls := 0
			closeCarrier := func() error { calls++; return nil }
			build := func(context.Context, string) (delivery.Result, error) { calls++; return delivery.Result{}, nil }
			switch scenario.name {
			case "missing context":
				ctx = nil
			case "missing cleanup":
				closeCarrier = nil
			case "missing builder":
				build = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "package escape":
				output = foreign
			case "evidence escape":
				evidence.directory = foreign
			case "same destination":
				evidence.directory = output
			case "package contains evidence":
				evidence.directory = filepath.Join(output, "child")
			case "evidence contains package":
				output = filepath.Join(evidence.directory, "child")
			}
			result, err := publishReconciledDiskPackage(ctx, workspace, output, evidence, closeCarrier, build)
			if factoryErrorCode(err) != scenario.code || !reflect.DeepEqual(result, delivery.Result{}) || calls != 0 {
				t.Fatalf("admission returned %+v, %v; callbacks=%d", result, err, calls)
			}
			entries, err := os.ReadDir(workspace)
			if err != nil || len(entries) != 0 {
				t.Fatalf("admission left workspace entries: %v, %v", entries, err)
			}
			preserved, err := os.ReadFile(foreign)
			if err != nil || string(preserved) != "caller-owned" {
				t.Fatalf("admission touched a foreign file: %q, %v", preserved, err)
			}
		})
	}
}
