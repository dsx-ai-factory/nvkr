package factory

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
)

func TestExecuteToolAcquireAcceptsExactRawExecutable(t *testing.T) {
	raw := []byte("static executable bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	digest := pipeline.FileDigest(raw)
	path := filepath.Join(t.TempDir(), "tools", "cosign")
	node := Node{ID: "tool/cosign", URL: server.URL, Digest: digest, ExecutableDigest: digest, ToolFormat: ToolFormatRaw}
	output := Artifact{ID: "tool/cosign", Path: path}
	if err := executeToolAcquire(context.Background(), node, output, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("raw tool bytes = %q, want %q", got, raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o555 {
		t.Fatalf("raw tool mode = %o, want 555", info.Mode().Perm())
	}
}

func TestExecuteToolAcquireRejectsCorruptRawExecutableWithoutOutput(t *testing.T) {
	raw := []byte("corrupt executable bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "tools", "cosign")
	wantDigest := pipeline.FileDigest([]byte("expected executable bytes"))
	node := Node{ID: "tool/cosign", URL: server.URL, Digest: wantDigest, ExecutableDigest: wantDigest, ToolFormat: ToolFormatRaw}
	err := executeToolAcquire(context.Background(), node, Artifact{ID: "tool/cosign", Path: path}, io.Discard)
	assertFactoryErrorCode(t, err, nkderr.CodeVerifyFailed)
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("raw tool output exists after digest failure: %v", statErr)
	}
}

func TestPlanRejectsInvalidRawToolShapeBeforeFetch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Node)
		code   nkderr.Code
	}{
		{
			name: "unknown format",
			mutate: func(node *Node) {
				node.ToolFormat = "zip"
			},
			code: nkderr.CodeUnpinnedSource,
		},
		{
			name: "raw executable digest differs",
			mutate: func(node *Node) {
				node.ToolFormat = ToolFormatRaw
			},
			code: nkderr.CodeUnverifiedRef,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
			for index := range plan.Graph.Nodes {
				if plan.Graph.Nodes[index].Kind == NodeAcquire {
					test.mutate(&plan.Graph.Nodes[index])
					assertFactoryErrorCode(t, plan.Validate(), test.code)
					return
				}
			}
			t.Fatal("signed plan has no tool acquisition node")
		})
	}
}

func assertFactoryErrorCode(t *testing.T, err error, want nkderr.Code) {
	t.Helper()
	var coded *nkderr.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error code = %v, want %s: %v", coded, want, err)
	}
}
