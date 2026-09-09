//go:build linux || darwin

package client_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	client "github.com/nhensley/nkdctl/client/v1"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestWithDataRejectsOversizedAndSymlinkAuthorityFiles(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "oversized",
			setup: func(t *testing.T, root string) {
				t.Helper()
				file, err := os.Create(filepath.Join(root, catalog.RegistryFile))
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(catalog.MaxCatalogFileBytes + 1); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, root string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), catalog.RegistryFile)
				if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, catalog.RegistryFile)); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			_, err := client.New(client.WithData(root)).ResolveRecipe(context.Background(), client.ResolveRequest{
				Criteria: criteria.Criteria{Role: "worker"},
			})
			if errorCode(err) != nkderr.CodeInvalidCatalog {
				t.Fatalf("WithData %s authority returned %v", test.name, err)
			}
		})
	}
}

func TestWithDataPreCanceledContextDoesNotResolveDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.New(client.WithData(filepath.Join(t.TempDir(), "missing"))).ResolveRecipe(ctx, client.ResolveRequest{
		Criteria: criteria.Criteria{Role: "worker"},
	})
	if errorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("pre-canceled WithData returned %v", err)
	}
}
