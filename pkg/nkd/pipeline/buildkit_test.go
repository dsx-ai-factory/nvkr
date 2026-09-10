package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

func TestBuildKitAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		launch    bool
		runtime   string
		qualified bool
		packaged  bool
		code      e.Code
	}{
		{name: "launched Docker", launch: true, runtime: "docker"},
		{name: "direct execution", runtime: "docker", code: e.CodeEngineCapability},
		{name: "unsupported runtime", launch: true, runtime: "podman", code: e.CodeEngineCapability},
		{name: "qualified", launch: true, runtime: "docker", qualified: true, code: e.CodeEngineCapability},
		{name: "legacy packaging", launch: true, runtime: "docker", packaged: true, code: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := sampleGraph().Nodes[0]
			node.BuilderType = ir.BuilderBuildKit
			node.Network = ir.NetworkDefault
			node.OutputType = "oci-image"
			opts := RunOptions{Ctx: context.Background(), Launch: tc.launch, Runtime: tc.runtime, DryRun: true, Package: tc.packaged, SourceRoot: filepath.Join(t.TempDir(), "missing")}
			if tc.qualified {
				opts.Qualification = &QualifiedExecution{}
			}
			_, err := (Local{}).ExecNode(node, opts)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("error=%v want %s", err, tc.code)
			}
		})
	}
}

func TestBuildKitOutputPromotionAndCleanup(t *testing.T) {
	for _, promote := range []bool{false, true} {
		t.Run(map[bool]string{false: "abort", true: "promote"}[promote], func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "collected")
			if err := os.Mkdir(out, 0o755); err != nil {
				t.Fatal(err)
			}
			mount, commit, cleanup, err := prepareBuildKitOutput(context.Background(), out)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			parent, err := os.Stat(filepath.Dir(mount))
			if err != nil {
				t.Fatal(err)
			}
			if parent.Mode().Perm() != 0o700 {
				t.Fatalf("private parent mode=%v", parent.Mode())
			}
			if err := os.WriteFile(filepath.Join(mount, "image.tar"), []byte("observed export"), 0o644); err != nil {
				t.Fatal(err)
			}
			if promote {
				if err := commit(); err != nil {
					t.Fatal(err)
				}
			}
			cleanup()
			if _, err := os.Stat(filepath.Dir(mount)); !os.IsNotExist(err) {
				t.Fatalf("staging parent remains: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(out, "image.tar"))
			if promote {
				if err != nil || string(raw) != "observed export" {
					t.Fatalf("promoted output=%q %v", raw, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("aborted output exposed: %v", err)
			}
		})
	}
}
