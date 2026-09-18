package pipeline

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

const maxResultBytes = 1 << 20

// ObserveResult reopens one producer's recorded result and its expected output.
// The caller supplies the paths from a validated execution plan; paths recorded by
// the producer never select bytes. This checks descriptive metadata and current
// bytes only, and issues no execution qualification or terminal receipt.
func ObserveResult(ctx context.Context, node ir.Node, payloadPath, resultPath string) (ArtifactResult, error) {
	if err := requireRunContext(ctx, "producer result observation"); err != nil {
		return ArtifactResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ArtifactResult{}, e.Wrap(e.CodeTimeout, err, "producer result observation canceled")
	}
	if node.Kind != ir.KindBuild && node.Kind != ir.KindPull {
		return ArtifactResult{}, e.New(e.CodeVerifyFailed, "node %q is not an artifact producer", node.ID)
	}
	result, err := readArtifactResultContext(ctx, resultPath)
	if err != nil {
		return ArtifactResult{}, err
	}
	if result.Component != node.Component || result.OutputType != node.OutputType ||
		!canonical.IsDigest(result.SHA256) || result.Size < 0 {
		return ArtifactResult{}, e.New(e.CodeVerifyFailed, "producer result %q has no exact component, output type or byte identity", node.ID)
	}
	if node.Kind == ir.KindBuild {
		if result.Toolchain != node.Toolchain || result.BuilderType != node.BuilderType || result.Path != node.ArtifactPath ||
			result.Ref != "" || result.Packaged {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed, "build result %q differs from its declared producer", node.ID)
		}
	} else {
		if result.SHA256 != node.Digest || result.EngineClass != ir.EnginePrebuiltAcquire {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed, "pull result %q differs from its pinned acquisition", node.ID)
		}
		if node.OutputType == "oci-image" {
			if result.Ref != node.Artifact+"@"+node.Digest {
				return ArtifactResult{}, e.New(e.CodeVerifyFailed, "pull result %q differs from its pinned image reference", node.ID)
			}
			size, err := verifyOCILayoutAndSize(ctx, payloadPath, node.Digest)
			if err != nil {
				return ArtifactResult{}, err
			}
			if result.Size != size {
				return ArtifactResult{}, e.New(e.CodeVerifyFailed, "pull result %q differs from its observed OCI closure size", node.ID)
			}
			result.TransportPath = payloadPath
			result.Path = result.Ref
			return result, nil
		}
		if result.Ref != "" || result.Packaged {
			return ArtifactResult{}, e.New(e.CodeVerifyFailed, "pull result %q does not describe a raw acquired file", node.ID)
		}
	}
	digest, size, _, err := hashRegularBeneathContext(ctx, filepath.Dir(payloadPath), filepath.Base(payloadPath))
	if err != nil {
		return ArtifactResult{}, mapCancellation(ctx, e.Wrap(e.CodeVerifyFailed, err, "observing producer %q bytes", node.ID), "producer result observation")
	}
	if result.SHA256 != digest || result.Size != size {
		return ArtifactResult{}, e.New(e.CodeVerifyFailed, "producer %q bytes differ from its recorded digest or size", node.ID)
	}
	result.AbsPath = payloadPath
	result.TransportPath = ""
	if node.Kind == ir.KindPull {
		result.Path = payloadPath
	}
	return result, nil
}

// VerifyResults is the terminal CI seam. It accepts only one strict result document
// for every graph producer, reconstructs storage locations from the graph and current
// workspace (never from producer-controlled absolute paths), and invokes the same
// authoritative byte verifier as Local.Run.
func (l Local) VerifyResults(g *ir.Graph, buildDigest, resultsDir string, opts RunOptions) (*Report, error) {
	if err := requireRunContext(opts.Ctx, "terminal result verification"); err != nil {
		return &Report{}, err
	}
	opts = withDefaults(opts)
	if err := validateGraphIdentity(g, buildDigest); err != nil {
		return &Report{}, err
	}
	included, err := selectNodes(g.Nodes, opts.Only)
	if err != nil {
		return &Report{}, err
	}
	selectedNodes := make([]ir.Node, 0, len(g.Nodes))
	for _, node := range g.Nodes {
		if included[node.ID] {
			selectedNodes = append(selectedNodes, node)
		}
	}
	if err := rejectUnsupportedExecutionRequirements(selectedNodes, "terminal result verification"); err != nil {
		return &Report{}, err
	}
	if resultsDir == "" {
		resultsDir = filepath.Join(filepath.Dir(opts.SourceRoot), "results")
	}
	rep := &Report{BuildDigest: buildDigest}
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
	results, _, err := securepath.OpenDirectory(resultsDir)
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
