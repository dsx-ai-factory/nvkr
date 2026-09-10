package catalog

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type countedLegacyProvider struct{ calls int }

func (provider *countedLegacyProvider) ReadFile(string) ([]byte, error) {
	provider.calls++
	return nil, fs.ErrNotExist
}
func (provider *countedLegacyProvider) ReadDir(string) ([]string, error) {
	provider.calls++
	return nil, nil
}
func (*countedLegacyProvider) Name() string { return "legacy" }

type countedContextProvider struct {
	legacyCalls  int
	contextCalls int
	file         []byte
	directory    []string
}

func (provider *countedContextProvider) ReadFile(string) ([]byte, error) {
	provider.legacyCalls++
	return nil, errors.New("legacy read must not be called")
}
func (provider *countedContextProvider) ReadDir(string) ([]string, error) {
	provider.legacyCalls++
	return nil, errors.New("legacy listing must not be called")
}
func (*countedContextProvider) Name() string { return "context" }
func (provider *countedContextProvider) ReadFileContext(ctx context.Context, _ string) ([]byte, error) {
	provider.contextCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return provider.file, nil
}
func (provider *countedContextProvider) ReadDirContext(ctx context.Context, _ string) ([]string, error) {
	provider.contextCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return provider.directory, nil
}

func catalogErrorCode(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestLoadContextRejectsCanceledContextBeforeProviderIO(t *testing.T) {
	provider := &countedContextProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadContext(ctx, provider); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled load returned %v", err)
	}
	if provider.contextCalls != 0 || provider.legacyCalls != 0 {
		t.Fatalf("pre-canceled load performed I/O: context=%d legacy=%d", provider.contextCalls, provider.legacyCalls)
	}
}

func TestLoadContextRejectsLegacyProviderBeforeIO(t *testing.T) {
	provider := &countedLegacyProvider{}
	_, err := LoadContext(context.Background(), provider)
	if catalogErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("legacy authority provider returned %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("legacy provider performed %d calls", provider.calls)
	}
}

func TestDirectoryProviderSupportsBoundedContextLoad(t *testing.T) {
	provider := Dir("../../../testdata/catalogs/kubernetes-layered")
	if _, ok := provider.(ContextDataProvider); !ok {
		t.Fatal("directory provider does not implement ContextDataProvider")
	}
	if _, err := LoadContext(context.Background(), provider); err != nil {
		t.Fatalf("context load of explicit catalog failed: %v", err)
	}
}

func TestBoundContextProviderEnforcesAggregateByteLimit(t *testing.T) {
	provider := &countedContextProvider{file: bytes.Repeat([]byte{'x'}, int(MaxCatalogFileBytes))}
	bound, err := BindContext(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	reads := int(MaxCatalogTotalBytes / MaxCatalogFileBytes)
	for index := 0; index < reads; index++ {
		if _, err := bound.ReadFile("registry.yaml"); err != nil {
			t.Fatalf("bounded read %d failed early: %v", index, err)
		}
	}
	if _, err := bound.ReadFile("registry.yaml"); catalogErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("aggregate overflow returned %v", err)
	}
	if provider.legacyCalls != 0 {
		t.Fatalf("context binding called legacy methods %d times", provider.legacyCalls)
	}
}

type cancelAfterReader struct {
	cancel context.CancelFunc
	done   bool
}

func (reader *cancelAfterReader) Read(destination []byte) (int, error) {
	if reader.done {
		return 0, errors.New("read continued after cancellation checkpoint")
	}
	reader.done = true
	copy(destination, "first chunk")
	reader.cancel()
	return len("first chunk"), nil
}

func TestReadCatalogFileObservesCancellationBetweenChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := readCatalogFile(ctx, &cancelAfterReader{cancel: cancel}, MaxCatalogFileBytes)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bounded read returned %v", err)
	}
}

func TestDirectoryListingValidationRejectsAdversarialResults(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
	}{
		{name: "escape", entries: []string{"../outside.yaml"}},
		{name: "duplicate", entries: []string{"a.yaml", "a.yaml"}},
		{name: "depth", entries: []string{strings.Repeat("a/", MaxCatalogTraversalDepth+1) + "x.yaml"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateDirectoryListing("overlays", test.entries); catalogErrorCode(err) != nkderr.CodeInvalidCatalog {
				t.Fatalf("adversarial listing returned %v", err)
			}
		})
	}
}
