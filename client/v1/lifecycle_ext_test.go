package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	client "github.com/nhensley/nkdctl/client/v1"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestSDKLifecyclePreparationIsOpaqueAndDryRunHasNoOutput(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	sdk := client.New()
	ctx := context.Background()
	prepared, err := sdk.PrepareLifecycleRun(ctx, "definitions/nke-v1/definition.yaml", "definitions/nke-v1/environment-local.yaml", "test", "sdk-attempt")
	if err != nil {
		t.Fatal(err)
	}
	original := prepared.Description()
	if original.Status != "pending" || original.Lane != "static-unqualified" || len(original.Expected) != 1 || len(original.Observed) != 0 {
		t.Fatalf("prepared Run = %+v", original)
	}
	modified := prepared.Description()
	modified.Expected[0].Components[0] = "substitute"
	modified.Expected[0].Name = "substitute"
	if got := prepared.Description(); got.Expected[0].Name != original.Expected[0].Name || got.Expected[0].Components[0] != original.Expected[0].Components[0] {
		t.Fatal("description mutation changed frozen requirements")
	}
	attempts := filepath.Join(".nkd", "sdk-test-"+filepath.Base(filepath.Dir(t.TempDir())))
	run, err := sdk.ExecuteLifecycleRun(ctx, prepared, client.LocalRunOptions{AttemptsRoot: attempts, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "pending" || run.PlanDigest != original.PlanDigest || len(run.Observed) != 0 {
		t.Fatalf("dry-run changed Run state: %+v", run)
	}
	if _, err := os.Stat(attempts); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created storage: %v", err)
	}
	raw, err := marshalOpaque(prepared)
	if err != nil {
		t.Fatal(err)
	}
	var restored client.PreparedRun
	if err := unmarshalOpaque(raw, &restored); err != nil {
		t.Fatal(err)
	}
	_, err = sdk.ExecuteLifecycleRun(ctx, &restored, client.LocalRunOptions{AttemptsRoot: attempts, DryRun: true})
	requireLifecycleCode(t, err, nkderr.CodeUsage)
	if _, err := os.Stat(attempts); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("serialized preparation created storage: %v", err)
	}
}

func marshalOpaque(value any) ([]byte, error) { return json.Marshal(value) }

func unmarshalOpaque(raw []byte, value any) error { return json.Unmarshal(raw, value) }

func TestSDKRejectsUnsupportedGenerationBeforeInputsAndUnconfiguredConsumer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated")
	_, err := client.New().GenerateLifecycleRun(context.Background(), "absent-definition.yaml", "absent-environment.yaml", "test", "tekton", root, false)
	requireLifecycleCode(t, err, nkderr.CodeEngineCapability)
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported engine created output: %v", err)
	}
	_, err = client.New().VerifyRelease(context.Background(), "registry.example.test/product@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		client.VerificationPolicy{Lane: "pipeline-attested", Scope: "artifact-publication", IdentityRegexp: "^builder$", Issuer: "issuer"})
	requireLifecycleCode(t, err, nkderr.CodeEngineCapability)
}

func requireLifecycleCode(t *testing.T, err error, expected nkderr.Code) {
	t.Helper()
	var coded *nkderr.Error
	if !errors.As(err, &coded) || coded.Code != expected {
		t.Fatalf("error = %v; want %s", err, expected)
	}
}
