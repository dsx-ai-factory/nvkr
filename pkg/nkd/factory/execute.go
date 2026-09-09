package factory

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/aicr"
	"github.com/nhensley/nkdctl/pkg/nkd/assemble"
	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/cluster"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
)

const (
	maxFactoryToolBytes         int64 = 512 << 20
	maxFactoryToolExpandedBytes int64 = 1 << 30
	maxFactoryToolEntries             = 4096
)

// LoadPlanContext admits the exact generated execution plan. The plan is not an
// authority receipt, but node execution must still reject a changed or unsafe file.
func LoadPlanContext(ctx context.Context, path string) (*Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "factory plan load requires a context")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "factory-plan", MaxBytes: MaxDefinitionBytes})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "opening factory plan"))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, MaxDefinitionBytes)
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "reading factory plan"))
	}
	var plan Plan
	if err := canonical.UnmarshalExact(raw, &plan); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "decoding factory plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return &plan, nil
}

// ExecuteNode executes a non-IR artifact-graph node. Fetch/build/pull nodes keep
// using exec-node over the exact recipe graph; this function owns factory tooling,
// unqualified terminal observation, packaging, and delivery representation.
func ExecuteNode(ctx context.Context, plan *Plan, nodeID string, stdout, stderr io.Writer) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "factory node execution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "factory node execution canceled")
	}
	if plan == nil {
		return e.New(e.CodeUsage, "factory node execution requires a plan")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	nodes, artifacts := planIndex(plan)
	node, ok := nodes[nodeID]
	if !ok {
		return e.New(e.CodeInvalidCatalog, "factory plan has no node %q", nodeID)
	}
	if node.IRNode != "" {
		return e.New(e.CodeUsage, "IR node %q must execute through exec-node", nodeID)
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	switch node.Kind {
	case NodeAcquire:
		return executeToolAcquire(ctx, node, artifacts[node.Outputs[0]], stdout)
	case NodeAICR:
		return executeAICR(ctx, plan, node, artifacts, stdout, stderr)
	case NodeVerify:
		return executeObservation(ctx, plan, node, artifacts)
	case NodeSBOM:
		return executeSBOM(ctx, plan, node, artifacts, stdout)
	case NodePackage:
		return executePackage(ctx, plan, node, artifacts)
	case NodePublish:
		return executePublish(ctx, plan, node, artifacts)
	case NodeComposition:
		return executeComposition(ctx, plan, node, artifacts)
	case NodeSign:
		return executeSign(ctx, plan, node, artifacts, productionOCISigner, stdout)
	case NodeVerifyPublication:
		return executeVerifyPublication(ctx, plan, node, artifacts, productionOCISigner, stdout)
	default:
		return e.New(e.CodeEngineCapability, "factory node kind %q has no executable adapter", node.Kind)
	}
}

func planIndex(plan *Plan) (map[string]Node, map[string]Artifact) {
	nodes := make(map[string]Node, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodes[node.ID] = node
	}
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	return nodes, artifacts
}

func executeToolAcquire(ctx context.Context, node Node, output Artifact, stdout io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, node.URL, nil)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "creating tool request")
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "downloading tool %s", node.ID))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return e.New(e.CodeUnavailable, "downloading tool %s returned HTTP %d", node.ID, response.StatusCode)
	}
	if response.ContentLength > maxFactoryToolBytes {
		return e.New(e.CodeVerifyFailed, "tool %s exceeds %d bytes", node.ID, maxFactoryToolBytes)
	}
	archive, err := os.CreateTemp("", "nkd-factory-tool-*.tar.gz")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating private tool archive")
	}
	archivePath := archive.Name()
	defer func() { _ = os.Remove(archivePath) }()
	defer func() { _ = archive.Close() }()
	hash := sha256.New()
	written, err := copyContext(ctx, io.MultiWriter(archive, hash), io.LimitReader(response.Body, maxFactoryToolBytes+1))
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "reading tool %s", node.ID))
	}
	if written > maxFactoryToolBytes {
		return e.New(e.CodeVerifyFailed, "tool %s exceeds %d bytes", node.ID, maxFactoryToolBytes)
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if got != node.Digest {
		return e.New(e.CodeVerifyFailed, "tool %s archive digest %s does not match %s", node.ID, got, node.Digest)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return e.Wrap(e.CodeInternal, err, "rewinding tool archive")
	}
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening tool archive")
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	var member []byte
	var expanded int64
	entries := 0
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return e.Wrap(e.CodeVerifyFailed, nextErr, "reading tool archive")
		}
		entries++
		if entries > maxFactoryToolEntries || header.Size < 0 || header.Size > maxFactoryToolExpandedBytes-expanded {
			return e.New(e.CodeVerifyFailed, "tool %s archive exceeds expanded size or entry bounds", node.ID)
		}
		expanded += header.Size
		name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(header.Name)), "./")
		if name != node.Executable {
			written, err := copyContext(ctx, io.Discard, io.LimitReader(tarReader, header.Size))
			if err != nil || written != header.Size {
				return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "discarding tool archive member"))
			}
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxFactoryToolBytes || member != nil {
			return e.New(e.CodeVerifyFailed, "tool %s executable member is repeated, non-regular, or oversized", node.ID)
		}
		var captured strings.Builder
		captured.Grow(int(header.Size))
		written, err := copyContext(ctx, &captured, io.LimitReader(tarReader, header.Size+1))
		if err != nil || written != header.Size {
			return e.Wrap(e.CodeVerifyFailed, err, "reading tool executable member")
		}
		member = []byte(captured.String())
	}
	if member == nil || pipeline.FileDigest(member) != node.ExecutableDigest {
		return e.New(e.CodeVerifyFailed, "tool %s executable does not match %s", node.ID, node.ExecutableDigest)
	}
	if err := writeFactoryFile(ctx, output.Path, member, 0o555); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "acquired %s: archive %s, executable %s\n", node.ID, node.Digest, node.ExecutableDigest)
	return nil
}

func executeComposition(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) error {
	name := strings.TrimPrefix(node.ID, "composition/")
	for _, composition := range plan.Compositions {
		if composition.Name != name {
			continue
		}
		raw, err := composition.Composition.Marshal()
		if err != nil {
			return err
		}
		return writeFactoryFile(ctx, artifacts[node.Outputs[0]].Path, append(raw, '\n'), 0o644)
	}
	return e.New(e.CodePipelineDrift, "composition node %q has no exact plan object", node.ID)
}

func executeAICR(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, stdout, stderr io.Writer) error {
	var recipePlan *RecipePlan
	for index := range plan.Recipes {
		if plan.Recipes[index].Name == node.RecipeTarget {
			recipePlan = &plan.Recipes[index]
			break
		}
	}
	if recipePlan == nil || recipePlan.Graph.AICR == nil || len(node.Inputs) != 1 {
		return e.New(e.CodePipelineDrift, "AICR node %q lacks recipe activation or generator input", node.ID)
	}
	activation := recipePlan.Graph.AICR
	profile := aicr.Profile{Identity: recipePlan.AICRProfileIdentity,
		Digest: activation.RecipeDigest, GeneratorDigest: activation.GeneratorDigest,
		Service: activation.Service, Accelerator: activation.Accelerator, OS: activation.OS,
		Intent: activation.Intent, Platform: activation.Platform}
	options := aicr.Options{OutputDir: artifacts[node.Outputs[0]].Path}
	if bundle := aicrBundleForRecipe(plan, recipePlan.RecipeDigest); bundle != nil {
		options.Deployer = bundle.Deployer
		if bundle.StorageClass != "" {
			options.ExtraBundleArgs = append(options.ExtraBundleArgs, "--storage-class", bundle.StorageClass)
		}
		for _, value := range bundle.AcceleratedNodeSelectors {
			options.ExtraBundleArgs = append(options.ExtraBundleArgs, "--accelerated-node-selector", value)
		}
		for _, value := range bundle.AcceleratedNodeTolerations {
			options.ExtraBundleArgs = append(options.ExtraBundleArgs, "--accelerated-node-toleration", value)
		}
		for _, value := range bundle.WorkloadSelectors {
			options.ExtraBundleArgs = append(options.ExtraBundleArgs, "--workload-selector", value)
		}
	}
	bundler := aicr.CLIBundler{Binary: artifacts[node.Inputs[0]].Path}
	qualified, err := bundler.BundleQualified(ctx, profile, options, stdout, stderr)
	if err != nil {
		return err
	}
	_, err = qualified.ValidateFor(ctx, profile)
	return err
}

func aicrBundleForRecipe(plan *Plan, digest string) *cluster.AICRBundleInputs {
	for index := range plan.Compositions {
		for _, binding := range plan.Compositions[index].Composition.Recipes {
			if binding.RecipeDigest == digest && binding.AICRBundle != nil {
				return binding.AICRBundle
			}
		}
	}
	return nil
}

type factoryObservation struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Qualification EvidenceTier           `json:"qualification"`
	RecipeDigest  string                 `json:"recipeDigest"`
	Inputs        []factoryObservedInput `json:"inputs"`
}

type factoryObservedInput struct {
	ArtifactID string `json:"artifactId"`
	Kind       string `json:"kind"`
	Digest     string `json:"digest"`
}

func executeObservation(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) error {
	observation := factoryObservation{SchemaVersion: "nkd.dev/factory-observation/v1", Qualification: TierStaticUnqualified}
	for _, recipePlan := range plan.Recipes {
		if recipePlan.Name == node.RecipeTarget {
			observation.RecipeDigest = recipePlan.RecipeDigest
		}
	}
	for _, input := range node.Inputs {
		artifact := artifacts[input]
		digest, err := digestFactoryPath(ctx, artifact.Path)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "observing factory input %s", input)
		}
		observation.Inputs = append(observation.Inputs, factoryObservedInput{ArtifactID: input, Kind: artifact.Kind, Digest: digest})
	}
	if observation.RecipeDigest == "" {
		return e.New(e.CodePipelineDrift, "terminal node %q has no recipe digest", node.ID)
	}
	raw, err := canonical.Marshal(observation)
	if err != nil {
		return err
	}
	return writeFactoryFile(ctx, artifacts[node.Outputs[0]].Path, append(raw, '\n'), 0o644)
}

func executePackage(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) error {
	output := artifacts[node.Outputs[0]]
	var inputs []Artifact
	for _, id := range node.Inputs {
		planned := Artifact{}
		for _, candidate := range plan.Graph.Artifacts {
			if candidate.ID == id {
				planned = candidate
				break
			}
		}
		if planned.ID == "" {
			return e.New(e.CodePipelineDrift, "package node %q has unknown input %q", node.ID, id)
		}
		if planned.Kind == string(TierTerminalObservation) || planned.Kind == "producer-result" {
			continue
		}
		artifact, ok := artifacts[id]
		if !ok || artifact.ID != planned.ID || artifact.Kind != planned.Kind || artifact.Name != planned.Name ||
			artifact.RecipeTarget != planned.RecipeTarget || artifact.Component != planned.Component {
			return e.New(e.CodeReleaseIncomplete, "package node %q has no exact declared input %q", node.ID, id)
		}
		inputs = append(inputs, artifact)
	}
	switch node.PackageKind {
	case PackageBinaryBundle:
		files := make([]delivery.BinaryFileInput, 0, len(inputs))
		for _, input := range inputs {
			files = append(files, delivery.BinaryFileInput{Name: input.Name, Path: input.Path,
				InstallPath: "/usr/local/bin/" + input.Name, Mode: 0o755})
		}
		_, err := delivery.BuildBinaryBundle(ctx, output.Path, delivery.BinaryBundleRequest{
			OS: "linux", Arch: packageArch(plan, inputs), Purpose: output.Name, Files: files,
		})
		return err
	case PackageAPT:
		packages := make([]delivery.DebianPackageInput, 0, len(inputs))
		staging := filepath.Join(filepath.Dir(output.Path), "."+output.Name+"-deb-inputs")
		if err := os.RemoveAll(staging); err != nil {
			return e.Wrap(e.CodeInternal, err, "resetting APT package staging")
		}
		if err := os.MkdirAll(staging, 0o700); err != nil {
			return e.Wrap(e.CodeInternal, err, "creating APT package staging")
		}
		defer func() { _ = os.RemoveAll(staging) }()
		for _, input := range inputs {
			version, epoch, arch, err := packageInputMetadata(plan, input)
			if err != nil {
				return err
			}
			deb := filepath.Join(staging, fmt.Sprintf("%s_%s_%s.deb", input.Name, version, arch))
			// Factory execution is explicitly unqualified (TierStaticUnqualified). This deterministic
			// projection makes the planned APT dataflow executable without claiming a
			// descriptor-held qualified package receipt.
			//nolint:staticcheck
			if _, err := assemble.Deb(input.Path, input.Name, version, epoch, arch, deb); err != nil {
				return e.Wrap(e.CodeBuildFailed, err, "assembling Debian package for %s", input.ID)
			}
			packages = append(packages, delivery.DebianPackageInput{Path: deb})
		}
		_, err := delivery.BuildAPTRepository(ctx, output.Path, delivery.APTRequest{
			Distribution: node.Distribution, Component: node.PackageComponent, Packages: packages,
		})
		return err
	case PackageHelm:
		if len(inputs) != 1 {
			return e.New(e.CodeInvalidCatalog, "Helm package %q requires exactly one AICR tree", node.ID)
		}
		archive := filepath.Join(filepath.Dir(output.Path), "."+output.Name+".aicr.tar")
		// The transport archive is an internal carrier, not a graph output. Always
		// reclaim it, including after interruption or a previous failed run, so an
		// owned factory workspace is genuinely rerunnable without weakening the
		// archive publisher's no-replace guarantee.
		if err := removeFactoryCarrier(archive); err != nil {
			return err
		}
		defer func() { _ = os.Remove(archive) }()
		root, relative, err := lifecycleTransportMember(plan, inputs[0])
		if err != nil {
			return err
		}
		if err := pipeline.PackTransport(ctx, archive, root, []string{relative}); err != nil {
			return err
		}
		digest, err := digestFactoryPath(ctx, archive)
		if err != nil {
			return err
		}
		_, err = delivery.BuildPublicationRoot(ctx, output.Path, delivery.PublicationRequest{Objects: []delivery.ObjectInput{{
			Name: output.Name, Kind: "aicr-bundle", Path: archive,
			ArtifactType: output.MediaType, LayerMediaType: "application/vnd.nkd.aicr-bundle.v1.tar",
			SemanticDigest: digest, BlobDigest: digest,
		}}})
		return err
	case PackageOCI:
		objects := make([]delivery.ObjectInput, 0, len(inputs))
		var temporaryArchives []string
		defer func() {
			for _, path := range temporaryArchives {
				_ = os.Remove(path)
			}
		}()
		for _, input := range inputs {
			if input.RecipeTarget != "" {
				continue
			}
			object := delivery.ObjectInput{Name: input.Name, Kind: input.Kind, Path: input.Path,
				ArtifactType: input.MediaType, LayerMediaType: input.MediaType}
			switch input.Kind {
			case PackageBinaryBundle:
				_, inspected, err := delivery.InspectBinaryBundle(ctx, input.Path)
				if err != nil {
					return err
				}
				object.Layout, object.SemanticDigest, object.BlobDigest = true, inspected.SemanticDigest, inspected.RootDigest
			case PackageHelm, PackageOCI:
				inspected, err := delivery.Inspect(ctx, input.Path)
				if err != nil {
					return err
				}
				semantic, err := inspected.Root.Digest()
				if err != nil {
					return err
				}
				object.Layout, object.SemanticDigest, object.BlobDigest = true, semantic, inspected.RootDigest
			case PackageAPT:
				repository, inspectErr := delivery.InspectAPTRepository(ctx, input.Path)
				if inspectErr != nil {
					return inspectErr
				}
				archive := filepath.Join(filepath.Dir(output.Path), "."+input.Name+".apt.tar")
				if err := removeFactoryCarrier(archive); err != nil {
					return err
				}
				if err := pipeline.PackTransport(ctx, archive, ".", []string{input.Path}); err != nil {
					return err
				}
				temporaryArchives = append(temporaryArchives, archive)
				digest, err := digestFactoryPath(ctx, archive)
				if err != nil {
					return err
				}
				object.Path, object.LayerMediaType = archive, "application/vnd.nkd.apt-repository.v1.tar"
				object.SemanticDigest, object.BlobDigest = repository.Digest, digest
			default:
				object.SemanticDigest, _ = compositionDigestForPath(plan, input.Path)
				if object.SemanticDigest == "" {
					object.SemanticDigest, _ = digestFactoryPath(ctx, input.Path)
				}
			}
			objects = append(objects, object)
		}
		if len(plan.LifecycleOutputs) > 0 {
			binding, err := lifecyclePublicationObject(ctx, plan, node, artifacts)
			if err != nil {
				return err
			}
			objects = append(objects, binding)
		}
		_, err := delivery.BuildPublicationRoot(ctx, output.Path, delivery.PublicationRequest{Objects: objects})
		return err
	default:
		return e.New(e.CodeEngineCapability, "factory package kind %q has no adapter", node.PackageKind)
	}
}

func lifecycleTransportMember(plan *Plan, observed Artifact) (string, string, error) {
	var canonicalPath string
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID == observed.ID {
			canonicalPath = filepath.ToSlash(filepath.Clean(artifact.Path))
			break
		}
	}
	prefix := filepath.ToSlash(filepath.Join(".nkd", "factory")) + "/"
	if canonicalPath == "" || !strings.HasPrefix(canonicalPath, prefix) {
		return "", "", e.New(e.CodePipelineDrift, "package input %q has no canonical factory path", observed.ID)
	}
	relative := strings.TrimPrefix(canonicalPath, prefix)
	observedClean := filepath.Clean(observed.Path)
	relativeNative := filepath.FromSlash(relative)
	if observedClean != relativeNative && !strings.HasSuffix(observedClean, string(filepath.Separator)+relativeNative) {
		return "", "", e.New(e.CodePipelineDrift, "package input %q moved outside its canonical workspace location", observed.ID)
	}
	root := strings.TrimSuffix(observedClean, relativeNative)
	root = strings.TrimSuffix(root, string(filepath.Separator))
	if root == "" {
		root = "."
	}
	return root, relative, nil
}

func removeFactoryCarrier(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "checking stale factory carrier %s", path)
	}
	if !info.Mode().IsRegular() {
		return e.New(e.CodeImmutableViolation,
			"stale factory carrier %s is not a regular file", path)
	}
	if err := os.Remove(path); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "removing stale factory carrier %s", path)
	}
	return nil
}

func packageInputMetadata(plan *Plan, artifact Artifact) (string, int64, string, error) {
	for _, recipePlan := range plan.Recipes {
		if recipePlan.Name != artifact.RecipeTarget || recipePlan.Graph == nil {
			continue
		}
		for _, node := range recipePlan.Graph.Nodes {
			if node.Component != artifact.Component || (node.Kind != ir.KindBuild && node.Kind != ir.KindPull) {
				continue
			}
			version := node.Version
			if version == "" {
				version = "0"
			}
			epoch := int64(0)
			for _, env := range node.Env {
				if env.Name == "SOURCE_DATE_EPOCH" {
					epoch, _ = strconv.ParseInt(env.Value, 10, 64)
					break
				}
			}
			arch := strings.TrimPrefix(node.Platform, "linux/")
			if epoch <= 0 || (arch != "amd64" && arch != "arm64") {
				return "", 0, "", e.New(e.CodePipelineDrift,
					"artifact %s lacks pinned Debian version/epoch/platform metadata", artifact.ID)
			}
			return version, epoch, arch, nil
		}
	}
	return "", 0, "", e.New(e.CodePipelineDrift, "artifact %s has no producing recipe node", artifact.ID)
}

func executePublish(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) error {
	if len(node.Inputs) != 1 || len(node.Outputs) != 1 {
		return e.New(e.CodePipelineDrift, "publish node %q is not unary", node.ID)
	}
	target := plan.Delivery.Targets[node.Target]
	if target.Kind != TargetOCI {
		return e.New(e.CodeEngineCapability, "factory publication target kind %q has no remote adapter", target.Kind)
	}
	username, password := os.Getenv("NKD_REGISTRY_USER"), os.Getenv("NKD_CREDENTIAL")
	if target.CredentialRef != "" && (username == "" || password == "") {
		return e.New(e.CodeTrustPolicyMissing,
			"factory publication target %q requires its configured username and credential", node.Target)
	}
	ref, err := delivery.Push(ctx, artifacts[node.Inputs[0]].Path, target.Ref, delivery.RegistryOptions{
		PlainHTTP: target.PlainHTTP, Anonymous: target.Anonymous,
		Username: username, Password: password,
	})
	if err != nil {
		return err
	}
	return writeFactoryFile(ctx, artifacts[node.Outputs[0]].Path, []byte(ref+"\n"), 0o644)
}

func packageArch(plan *Plan, inputs []Artifact) string {
	for _, input := range inputs {
		for _, recipePlan := range plan.Recipes {
			if recipePlan.Name == input.RecipeTarget {
				return recipePlan.Criteria.Arch
			}
		}
	}
	return "amd64"
}

func compositionDigestForPath(plan *Plan, path string) (string, bool) {
	for _, composition := range plan.Compositions {
		if filepath.ToSlash(filepath.Join(".nkd", "factory", "compositions", composition.Name+".json")) == path {
			return composition.Digest, true
		}
	}
	return "", false
}

type factoryTreeRow struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Digest string `json:"digest,omitempty"`
}

func digestFactoryPath(ctx context.Context, path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().IsRegular() {
		held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "factory-artifact", MaxBytes: 64 << 30})
		if err != nil {
			return "", err
		}
		defer func() { _ = held.Close() }()
		return held.Digest(), nil
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a regular file or directory", path)
	}
	var rows []factoryTreeRow
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(path, current)
		if err != nil || rel == "." {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		row := factoryTreeRow{Path: filepath.ToSlash(rel), Mode: uint32(info.Mode().Perm())}
		switch {
		case entry.IsDir():
			row.Kind = "directory"
		case info.Mode().IsRegular():
			row.Kind = "file"
			held, err := heldfile.OpenContext(ctx, current, heldfile.Options{Label: "factory-tree-entry", MaxBytes: 64 << 30})
			if err != nil {
				return err
			}
			row.Digest = held.Digest()
			if err := held.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("tree contains unsupported entry %s", rel)
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return canonical.Digest(canonical.KindFactoryObservation, rows)
}

func writeFactoryFile(ctx context.Context, path string, raw []byte, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "writing factory output canceled")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating factory output parent")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".nkd-factory-*")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating factory output")
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "publishing factory output canceled")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return e.Wrap(e.CodeInternal, err, "publishing factory output")
	}
	directory, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	return err
}
