package client_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	client "github.com/NVIDIA/nke-recipes/client/v1"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

func TestSDKEnvironmentLoadPreservesDocumentIdentity(t *testing.T) {
	path := filepath.Join("..", "..", "definitions", "nke-v1", "environment-local.yaml")
	want, err := lifecycle.LoadEnvironment(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.New(client.WithRuntime("podman")).LoadEnvironment(t.Context(), path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %+v, %v; want %+v", got, err, want)
	}
	wantDigest, err := want.Digest()
	if err != nil {
		t.Fatal(err)
	}
	gotDigest, err := got.Digest()
	if err != nil || gotDigest != wantDigest {
		t.Fatalf("identity = %q, %v; want %q", gotDigest, err, wantDigest)
	}
}

func TestSDKDocumentLoadRejectsInvalidInputsWithoutPartialModels(t *testing.T) {
	for _, tc := range []struct {
		name, raw        string
		missing, symlink bool
	}{
		{name: "missing", missing: true},
		{name: "malformed", raw: "metadata: ["},
		{name: "unknown field", raw: "apiVersion: unknown\nkind: Environment\nunrecognized: true\n"},
		{name: "invalid model", raw: "apiVersion: unknown\nkind: Environment\n"},
		{name: "symlink", raw: "{}", symlink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "document.yaml")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.symlink {
				if err := os.Symlink(path, path+".link"); err != nil {
					t.Fatal(err)
				}
				path += ".link"
			}
			sdk := client.New()
			environment, err := sdk.LoadEnvironment(t.Context(), path)
			requireLifecycleCode(t, err, e.CodeInvalidCatalog)
			if !reflect.DeepEqual(environment, client.Environment{}) {
				t.Fatal("partial environment escaped")
			}
			definition, err := sdk.LoadDefinition(t.Context(), path)
			requireLifecycleCode(t, err, e.CodeInvalidCatalog)
			if !reflect.DeepEqual(definition, client.Definition{}) {
				t.Fatal("partial definition escaped")
			}
		})
	}
}

func TestSDKLifecycleAdmissionPrecedesFileAccess(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sdk      *client.Client
		canceled bool
		code     e.Code
	}{
		{"nil client", nil, false, e.CodeUsage},
		{"invalid runtime", client.New(client.WithRuntime("")), false, e.CodeUsage},
		{"canceled", client.New(), true, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			_, err := tc.sdk.LoadEnvironment(ctx, "absent")
			requireLifecycleCode(t, err, tc.code)
			_, err = tc.sdk.LoadDefinition(ctx, "absent")
			requireLifecycleCode(t, err, tc.code)
			_, err = tc.sdk.PrepareLifecycleRun(ctx, "absent", "absent", "test", "attempt")
			requireLifecycleCode(t, err, tc.code)
			_, err = tc.sdk.GenerateLifecycleRun(ctx, "absent", "absent", "test", "github", "absent", true)
			requireLifecycleCode(t, err, tc.code)
		})
	}
}
