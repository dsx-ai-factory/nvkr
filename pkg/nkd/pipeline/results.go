package pipeline

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

const maxResultBytes = 1 << 20

// VerifyResults is the terminal CI seam. It accepts only one strict result document
// for every graph producer, reconstructs storage locations from the graph and current
// workspace (never from producer-controlled absolute paths), and invokes the same
// authoritative byte verifier as Local.Run.
func (l Local) VerifyResults(g *ir.Graph, recipeDigest, resultsDir string, opts RunOptions) (*Report, error) {
	if err := requireRunContext(opts.Ctx, "terminal result verification"); err != nil {
		return &Report{}, err
	}
	opts = withDefaults(opts)
	if err := validateGraphIdentity(g, recipeDigest); err != nil {
		return &Report{}, err
	}
	included, err := selectNodes(g.Nodes, opts.Only)
	if err != nil {
		return &Report{}, err
	}
	if resultsDir == "" {
		resultsDir = filepath.Join(filepath.Dir(opts.SourceRoot), "results")
	}
	rep := &Report{RecipeID: g.RecipeID, RecipeDigest: recipeDigest}
	producers := map[string]ir.Node{}
	var buildNodes []ir.Node
	for _, n := range g.Nodes {
		if included[n.ID] && (n.Kind == ir.KindBuild || n.Kind == ir.KindPull) {
			producers[n.ID] = n
		}
		if included[n.ID] && n.Kind == ir.KindBuild {
			buildNodes = append(buildNodes, n)
		}
	}
	if len(buildNodes) > 0 {
		if opts.Qualification == nil {
			return rep, e.New(e.CodePreflightFailed,
				"terminal CI verification contains from-source results but no execution-attestation authority")
		}
		verified, runtimePath, qualifyErr := qualifySelectedExecution(
			opts.ctx(), opts.Qualification, opts.Runtime, buildNodes, false,
		)
		if qualifyErr != nil {
			return rep, qualifyErr
		}
		opts.execution, opts.Runtime = verified, runtimePath
		defer verified.cleanupRuntime()
	}
	ids := make([]string, 0, len(producers))
	expectedFiles := make(map[string]bool, len(producers))
	for id := range producers {
		ids = append(ids, id)
		expectedFiles[sanitizeName(id)+".json"] = true
	}
	sort.Strings(ids)
	results, _, err := openDirectoryNoFollow(resultsDir)
	if err != nil {
		return rep, e.Wrap(e.CodeVerifyFailed, err, "reading terminal result directory %s", resultsDir)
	}
	entries, readErr := readDirectoryEntriesBounded(results, len(expectedFiles)+1)
	closeErr := results.Close()
	if readErr != nil {
		return rep, e.Wrap(e.CodeVerifyFailed, readErr, "reading terminal result directory %s", resultsDir)
	}
	if closeErr != nil {
		return rep, e.Wrap(e.CodeVerifyFailed, closeErr, "closing terminal result directory %s", resultsDir)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") || !expectedFiles[ent.Name()] {
			return rep, e.New(e.CodeVerifyFailed, "terminal result directory contains unexpected entry %q", ent.Name())
		}
	}
	if len(entries) != len(expectedFiles) {
		return rep, e.New(e.CodeVerifyFailed, "terminal result set is incomplete: got %d files want %d", len(entries), len(expectedFiles))
	}
	for _, id := range ids {
		if err := opts.ctx().Err(); err != nil {
			return rep, e.Wrap(e.CodeTimeout, err, "terminal result verification canceled")
		}
		n := producers[id]
		resultFile := filepath.Join(resultsDir, sanitizeName(id)+".json")
		a, err := readArtifactResultContext(opts.ctx(), resultFile)
		if err != nil {
			return rep, err
		}
		if err := bindResultToNode(&a, n, opts); err != nil {
			return rep, err
		}
		rep.Artifacts = append(rep.Artifacts, a)
	}
	if len(rep.Artifacts) != len(producers) {
		return rep, e.New(e.CodeVerifyFailed, "terminal result set is incomplete: got %d want %d", len(rep.Artifacts), len(producers))
	}
	sort.Slice(rep.Artifacts, func(i, j int) bool { return rep.Artifacts[i].Component < rep.Artifacts[j].Component })
	terminal, err := issueTerminalReceipt(opts.ctx(), rep, producers, included, opts)
	if err != nil {
		return rep, err
	}
	if len(buildNodes) > 0 {
		if err := bindQualifiedReport(opts.ctx(), rep, opts.execution, terminal); err != nil {
			return rep, err
		}
	} else if err := bindProfileFreeReport(opts.ctx(), rep, terminal); err != nil {
		return rep, err
	}
	return rep, nil
}

func readArtifactResultContext(ctx context.Context, path string) (ArtifactResult, error) {
	var result ArtifactResult
	raw, err := readRegularLimitedContext(ctx, path, maxResultBytes)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return result, e.Wrap(e.CodeTimeout, ctx.Err(), "reading producer result %s", path)
		}
		return result, e.Wrap(e.CodeVerifyFailed, err, "reading producer result %s", path)
	}
	if len(raw) > maxResultBytes {
		return result, e.New(e.CodeVerifyFailed, "producer result %s exceeds %d bytes", path, maxResultBytes)
	}
	if err := canonical.UnmarshalExact(raw, &result); err != nil {
		return result, e.Wrap(e.CodeVerifyFailed, err, "decoding producer result %s", path)
	}
	return result, nil
}

func bindResultToNode(a *ArtifactResult, n ir.Node, opts RunOptions) error {
	if a.Component != n.Component || a.OutputType != n.OutputType {
		return e.New(e.CodeVerifyFailed, "producer result for node %q identifies %q/%q, want %q/%q", n.ID, a.Component, a.OutputType, n.Component, n.OutputType)
	}
	switch n.Kind {
	case ir.KindBuild:
		if a.Qualification != ArtifactQualifiedExecution {
			return e.New(e.CodeVerifyFailed,
				"build result %q is explicitly %q, want %q", n.ID, a.Qualification, ArtifactQualifiedExecution)
		}
		if a.Toolchain != n.Toolchain || a.BuilderType != n.BuilderType || a.Path != n.ArtifactPath {
			return e.New(e.CodeVerifyFailed, "build result %q does not match its toolchain/builder/artifact declaration", n.ID)
		}
		if a.InputDigest == "" || a.EngineClass == "" {
			return e.New(e.CodeVerifyFailed, "build result %q lacks observed input or engine identity", n.ID)
		}
		if opts.execution == nil {
			return e.New(e.CodePreflightFailed, "build result %q has no qualified execution receipt", n.ID)
		}
		identity, ok := observedNodeIdentity(opts.execution, n.ID)
		if !ok || a.EngineClass != identity.EngineClass ||
			a.ProfileDigest != opts.execution.receipt.ProfileDigest() ||
			a.ExecutionBinding != opts.execution.receipt.ExecutionBinding() {
			return e.New(e.CodeVerifyFailed,
				"build result %q is not bound to the exact qualified execution", n.ID)
		}
		a.AbsPath = artifactHostPath(opts, n)
	case ir.KindPull:
		if a.Qualification != ArtifactProfileFreeAcquisition {
			return e.New(e.CodeVerifyFailed,
				"pull result %q has qualification %q, want %q", n.ID, a.Qualification, ArtifactProfileFreeAcquisition)
		}
		if a.SHA256 != n.Digest || a.EngineClass != ir.EnginePrebuiltAcquire {
			return e.New(e.CodeVerifyFailed, "pull result %q does not match its pinned digest/acquisition engine", n.ID)
		}
		if n.OutputType == "oci-image" {
			wantRef := n.Artifact + "@" + n.Digest
			if a.Ref != wantRef {
				return e.New(e.CodeVerifyFailed, "pull result %q ref %q does not match %q", n.ID, a.Ref, wantRef)
			}
			layoutPath := filepath.Join(prebuiltDir(opts, n), sanitizeName(n.Component)+".oci")
			if _, err := os.Stat(layoutPath); err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "missing OCI transport layout for %s", n.Component)
			}
			if err := opts.OCI.Verify(opts.ctx(), wantRef, layoutPath); err != nil {
				return err
			}
			a.TransportPath = layoutPath
		} else {
			name := n.ArtifactPath
			if name == "" {
				name = n.Component
			}
			abs, err := pathWithin(prebuiltDir(opts, n), name)
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "resolving transported artifact for %s", n.Component)
			}
			a.AbsPath = abs
		}
	default:
		return e.New(e.CodeInternal, "node %q is not an artifact producer", n.ID)
	}
	if !strings.HasPrefix(a.SHA256, "sha256:") {
		return e.New(e.CodeVerifyFailed, "producer result %q has malformed artifact digest %q", n.ID, a.SHA256)
	}
	hexDigest := strings.TrimPrefix(a.SHA256, "sha256:")
	if len(hexDigest) != 64 {
		return e.New(e.CodeVerifyFailed, "producer result %q has malformed artifact digest %q", n.ID, a.SHA256)
	}
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return e.New(e.CodeVerifyFailed, "producer result %q has malformed artifact digest %q", n.ID, a.SHA256)
	}
	return nil
}

// ResultDirectory is the stable workspace path used by emitted jobs.
func ResultDirectory(sourceRoot string) string {
	if sourceRoot == "" {
		sourceRoot = filepath.Join(".nkd", "work", "src")
	}
	return filepath.Join(filepath.Dir(sourceRoot), "results")
}
