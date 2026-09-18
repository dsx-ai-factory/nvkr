package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func writeObservedResult(t *testing.T, path string, result ArtifactResult) {
	t.Helper()
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestObserveResultChecksAcquiredOCIClosure(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string, *ir.Node, *ArtifactResult)
	}{
		{name: "exact"},
		{name: "wrong recorded pin", change: func(_ *testing.T, _ string, _ *ir.Node, r *ArtifactResult) { r.SHA256 = "sha256:" + repeatHex("a") }},
		{name: "wrong selected pin", change: func(_ *testing.T, _ string, n *ir.Node, r *ArtifactResult) {
			n.Digest = "sha256:" + repeatHex("a")
			r.SHA256, r.Ref = n.Digest, n.Artifact+"@"+n.Digest
		}},
		{name: "wrong reference", change: func(_ *testing.T, _ string, _ *ir.Node, r *ArtifactResult) {
			r.Ref = "registry.example/another@" + r.SHA256
		}},
		{name: "wrong size", change: func(_ *testing.T, _ string, _ *ir.Node, r *ArtifactResult) { r.Size++ }},
		{name: "wrong acquisition engine", change: func(_ *testing.T, _ string, _ *ir.Node, r *ArtifactResult) { r.EngineClass = "claimed-build" }},
		{name: "corrupt referenced blob", change: func(t *testing.T, root string, _ *ir.Node, _ *ArtifactResult) {
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", minimalOCIConfigDigest()[len("sha256:"):]), []byte("corrupt"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing referenced blob", change: func(t *testing.T, root string, _ *ir.Node, _ *ArtifactResult) {
			if err := os.Remove(filepath.Join(root, "blobs", "sha256", minimalOCIConfigDigest()[len("sha256:"):])); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unreferenced blob", change: func(t *testing.T, root string, _ *ir.Node, _ *ArtifactResult) {
			raw := []byte("undeclared payload")
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", digestBytes(raw)[len("sha256:"):]), raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing expected layout ignores supplied transport", change: func(t *testing.T, root string, _ *ir.Node, _ *ArtifactResult) {
			if err := os.Rename(root, root+".moved"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := t.TempDir()
			payload := filepath.Join(work, "expected.oci")
			digest := writeMinimalOCILayout(t, payload)
			size, err := verifyOCILayoutAndSize(t.Context(), payload, digest)
			if err != nil {
				t.Fatal(err)
			}
			// A valid alternate layout must never rescue corrupt or absent planned
			// bytes. Its path is producer-controlled descriptive data.
			alternate := filepath.Join(work, "alternate.oci")
			writeMinimalOCILayout(t, alternate)
			node := ir.Node{ID: "pull-image", Kind: ir.KindPull, Component: "image", OutputType: "oci-image",
				Artifact: "registry.example/image", Digest: digest}
			result := ArtifactResult{Component: node.Component, OutputType: node.OutputType,
				Qualification: ArtifactUnqualified, SHA256: digest, Size: size,
				EngineClass: ir.EnginePrebuiltAcquire, Ref: node.Artifact + "@" + digest, TransportPath: alternate}
			if test.change != nil {
				test.change(t, payload, &node, &result)
			}
			recordPath := filepath.Join(work, "result.json")
			writeObservedResult(t, recordPath, result)
			got, err := ObserveResult(t.Context(), node, payload, recordPath)
			if test.change != nil {
				if code(err) != e.CodeVerifyFailed {
					t.Fatalf("error = %v, want %s", err, e.CodeVerifyFailed)
				}
				return
			}
			if err != nil || got.TransportPath != payload || got.Path != got.Ref || got.SHA256 != digest || got.Size != size || got.Qualification != ArtifactUnqualified {
				t.Fatalf("observation changed bytes, carrier or qualification: %+v, %v", got, err)
			}
		})
	}
}

func TestObserveResultChecksExactBuildFile(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ArtifactResult)
		dirty  bool
	}{
		{name: "exact"},
		{name: "component", change: func(r *ArtifactResult) { r.Component = "sibling" }},
		{name: "output type", change: func(r *ArtifactResult) { r.OutputType = "oci-image" }},
		{name: "toolchain", change: func(r *ArtifactResult) { r.Toolchain = "other@sha256:" + repeatHex("a") }},
		{name: "builder type", change: func(r *ArtifactResult) { r.BuilderType = ir.BuilderNix }},
		{name: "relative path", change: func(r *ArtifactResult) { r.Path = "out/sibling" }},
		{name: "digest", change: func(r *ArtifactResult) { r.SHA256 = "sha256:" + repeatHex("a") }},
		{name: "malformed digest", change: func(r *ArtifactResult) { r.SHA256 = "not-a-digest" }},
		{name: "size", change: func(r *ArtifactResult) { r.Size++ }},
		{name: "negative size", change: func(r *ArtifactResult) { r.Size = -1 }},
		{name: "packaged", change: func(r *ArtifactResult) { r.Packaged = true }},
		{name: "reference", change: func(r *ArtifactResult) { r.Ref = "registry.example/image@" + r.SHA256 }},
		{name: "corrupt planned file ignores supplied transport", dirty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			work := t.TempDir()
			payload, alternate := filepath.Join(work, "expected"), filepath.Join(work, "alternate")
			raw := []byte("built bytes\n")
			for _, path := range []string{payload, alternate} {
				if err := os.WriteFile(path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			node := ir.Node{ID: "build-tool", Kind: ir.KindBuild, Component: "tool", OutputType: "host-package",
				ArtifactPath: "out/tool", BuilderType: ir.BuilderOCIHermetic,
				Toolchain: "toolchain@sha256:" + repeatHex("b")}
			result := ArtifactResult{Component: node.Component, OutputType: node.OutputType,
				Qualification: ArtifactUnqualified, Toolchain: node.Toolchain, BuilderType: node.BuilderType,
				Path: node.ArtifactPath, SHA256: digestBytes(raw), Size: int64(len(raw)), TransportPath: alternate}
			if test.change != nil {
				test.change(&result)
			}
			if test.dirty {
				if err := os.WriteFile(payload, []byte("wrong bytes\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			recordPath := filepath.Join(work, "result.json")
			writeObservedResult(t, recordPath, result)
			got, err := ObserveResult(t.Context(), node, payload, recordPath)
			if test.change != nil || test.dirty {
				if code(err) != e.CodeVerifyFailed {
					t.Fatalf("error = %v, want %s", err, e.CodeVerifyFailed)
				}
				return
			}
			if err != nil || got.AbsPath != payload || got.TransportPath != "" || got.SHA256 != digestBytes(raw) || got.Qualification != ArtifactUnqualified {
				t.Fatalf("observation = %+v, %v", got, err)
			}
		})
	}
}

func TestObserveResultRequiresLiveContextBeforeReading(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		want e.Code
	}{
		{name: "nil", want: e.CodeUsage},
		{name: "canceled", ctx: ctx, want: e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ObserveResult(test.ctx, ir.Node{}, "missing-payload", "missing-result")
			if code(err) != test.want {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
		})
	}
}
