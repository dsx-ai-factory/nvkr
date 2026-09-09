package client_test

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	client "github.com/nhensley/nkdctl/client/v1"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

type legacyCatalogProvider struct{ calls int }

func (provider *legacyCatalogProvider) ReadFile(string) ([]byte, error) {
	provider.calls++
	return nil, fs.ErrNotExist
}
func (provider *legacyCatalogProvider) ReadDir(string) ([]string, error) {
	provider.calls++
	return nil, nil
}
func (*legacyCatalogProvider) Name() string { return "legacy" }

func TestWithProviderRejectsLegacyIOBeforeRead(t *testing.T) {
	provider := &legacyCatalogProvider{}
	_, err := client.New(client.WithProvider(provider)).ResolveRecipe(context.Background(), client.ResolveRequest{
		Criteria: criteria.Criteria{Role: "worker"},
	})
	if errorCode(err) != nkderr.CodeUsage {
		t.Fatalf("legacy provider returned %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("legacy provider performed %d reads", provider.calls)
	}
}

type contextOnlyProvider struct {
	base         catalog.ContextDataProvider
	legacyCalls  int
	contextCalls int
}

func (provider *contextOnlyProvider) ReadFile(string) ([]byte, error) {
	provider.legacyCalls++
	return nil, errors.New("legacy catalog read called")
}
func (provider *contextOnlyProvider) ReadDir(string) ([]string, error) {
	provider.legacyCalls++
	return nil, errors.New("legacy catalog listing called")
}
func (*contextOnlyProvider) Name() string { return "context-only" }
func (provider *contextOnlyProvider) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	provider.contextCalls++
	return provider.base.ReadFileContext(ctx, name)
}
func (provider *contextOnlyProvider) ReadDirContext(ctx context.Context, name string) ([]string, error) {
	provider.contextCalls++
	return provider.base.ReadDirContext(ctx, name)
}

func TestWithProviderUsesOnlyDeepContextInterface(t *testing.T) {
	base, ok := catalog.Dir(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")).(catalog.ContextDataProvider)
	if !ok {
		t.Fatal("directory catalog is not context-aware")
	}
	provider := &contextOnlyProvider{base: base}
	if _, err := client.New(client.WithProvider(provider)).ResolveRecipe(context.Background(), client.ResolveRequest{
		Criteria: criteria.Criteria{Role: "worker"},
	}); err != nil {
		t.Fatalf("context provider resolution failed: %v", err)
	}
	if provider.contextCalls == 0 || provider.legacyCalls != 0 {
		t.Fatalf("provider calls: context=%d legacy=%d", provider.contextCalls, provider.legacyCalls)
	}
}

type adversarialCatalogIO struct {
	base      catalog.ContextDataProvider
	oversized bool
	listing   []string
}

func (adapter adversarialCatalogIO) ReadFile(ctx context.Context, name string) ([]byte, error) {
	if adapter.oversized && name == catalog.RegistryFile {
		return make([]byte, catalog.MaxCatalogFileBytes+1), nil
	}
	return adapter.base.ReadFileContext(ctx, name)
}

func (adapter adversarialCatalogIO) ReadDir(ctx context.Context, name string) ([]string, error) {
	if adapter.listing != nil && name == "overlays" {
		return adapter.listing, nil
	}
	return adapter.base.ReadDirContext(ctx, name)
}

func (adversarialCatalogIO) Name() string { return "adversarial" }

func TestWithCatalogIORejectsOversizedPayload(t *testing.T) {
	base := catalog.Dir(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")).(catalog.ContextDataProvider)
	_, err := client.New(client.WithCatalogIO(adversarialCatalogIO{base: base, oversized: true})).ResolveRecipe(
		context.Background(), client.ResolveRequest{Criteria: criteria.Criteria{Role: "worker"}})
	if errorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), "byte bound") {
		t.Fatalf("oversized CatalogIO payload returned %v", err)
	}
}

func TestWithCatalogIORejectsUnsafeListing(t *testing.T) {
	base := catalog.Dir(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")).(catalog.ContextDataProvider)
	_, err := client.New(client.WithCatalogIO(adversarialCatalogIO{base: base, listing: []string{"../outside.yaml"}})).ResolveRecipe(
		context.Background(), client.ResolveRequest{Criteria: criteria.Criteria{Role: "worker"}})
	if errorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe CatalogIO listing returned %v", err)
	}
}
