// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/releaseasset"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const (
	maxFactoryToolBytes         int64 = 512 << 20
	maxFactoryToolExpandedBytes int64 = 1 << 30
	maxFactoryToolEntries             = 4096
)

// LoadPlanContext admits the exact generated execution plan. The plan is not an
// authority receipt, but node execution must still reject a changed or unsafe file.
func LoadPlanContext(ctx context.Context, path string) (*Plan, error) {
	plan, _, err := loadPlanDocument(ctx, path)
	return plan, err
}

// loadPlanDocument retains the exact admitted document's blob integrity separately
// from producer Build identity. No second read can describe a different Plan.
func loadPlanDocument(ctx context.Context, path string) (*Plan, string, error) {
	if ctx == nil {
		return nil, "", e.New(e.CodeUsage, "factory plan load requires a context")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "run-plan", MaxBytes: MaxPlanBytes})
	if err != nil {
		return nil, "", classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "opening factory plan"))
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, MaxPlanBytes)
	if err != nil {
		return nil, "", classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "reading factory plan"))
	}
	var plan Plan
	if err := canonical.UnmarshalExact(raw, &plan); err != nil {
		return nil, "", e.Wrap(e.CodePipelineDrift, err, "decoding factory plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, "", err
	}
	return &plan, held.Digest(), nil
}

// ExecuteNode validates and executes one operation from the complete Plan.
// Producer instructions remain owned by the embedded build IR; all callers use
// the same dispatch for acquisition, builds, observation, packaging and publication.
func ExecuteNode(ctx context.Context, plan *Plan, nodeID string, stdout, stderr io.Writer) error {
	node, err := admitExecutionNode(ctx, plan, nodeID)
	if err != nil {
		return err
	}
	if githubDiskOutput(plan, node) != "" {
		return e.New(e.CodeEngineCapability, "ARC disk operations require the original grouped executor")
	}
	if err := rejectUnsupportedRuntimeStorage(plan, "factory execute-node"); err != nil {
		return err
	}
	_, artifacts := planIndex(plan)
	if len(plan.Environment.GitHubRunners) > 0 {
		if err := admitGitHubNodePlatform(plan, node, runtime.GOOS+"/"+runtime.GOARCH); err != nil {
			return err
		}
		// Producer admission runs here; hosted utility admission runs on the host
		// in executeGitHubNode before entering a container without daemon access.
		if node.IRNode != "" {
			if err := checkNodeContainerPlatform(ctx, plan, node, pipeline.CheckContainerPlatform); err != nil {
				return err
			}
		}
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if node.IRNode != "" {
		return executeProducer(ctx, plan, node, plan.Environment.RuntimeBinary(), 1, stdout, stderr, (pipeline.Local{}).ExecNode, nil)
	}
	switch node.Kind {
	case NodeAcquire:
		return executeToolAcquire(ctx, node, artifacts[node.Outputs[0]], stdout)
	case NodeVerify:
		return executeObservation(ctx, plan, node, artifacts)
	case NodeSBOM:
		return executeSBOM(ctx, plan, node, artifacts, stdout)
	case NodeAssembleRelease:
		return executeReferenceRelease(ctx, plan, node, artifacts, productionOCISigner)
	case NodePackage:
		return executePackage(ctx, plan, node, artifacts, stdout)
	case NodePublish:
		return executePublish(ctx, plan, node, artifacts, stdout)
	case NodeAttestEvidence:
		return executeAttestEvidence(ctx, plan, node, artifacts, productionOCISigner, stdout)
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
	if _, bounded := ctx.Deadline(); bounded {
		client.Timeout = 0
	}
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
	archive, err := os.CreateTemp("", "nvkr-factory-tool-*.tar.gz")
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
	// Official tools may publish one directly executable static binary instead of
	// wrapping it in an archive. The explicit raw format selects that shape; the
	// model validator separately requires equal transport and executable digests.
	if node.ToolFormat == ToolFormatRaw {
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			return e.Wrap(e.CodeInternal, err, "rewinding raw tool")
		}
		member, err := io.ReadAll(io.LimitReader(archive, maxFactoryToolBytes+1))
		if err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeInternal, err, "reading raw tool"))
		}
		if int64(len(member)) > maxFactoryToolBytes {
			return e.New(e.CodeVerifyFailed, "tool %s exceeds %d bytes", node.ID, maxFactoryToolBytes)
		}
		if err := writeFactoryFile(ctx, output.Path, member, 0o555); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "acquired %s: raw executable %s\n", node.ID, node.ExecutableDigest)
		return nil
	}
	if node.ToolFormat != ToolFormatTarGzip {
		return e.New(e.CodeInvalidCatalog, "tool %s has unsupported format %q", node.ID, node.ToolFormat)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return e.Wrap(e.CodeInternal, err, "rewinding tool archive")
	}
	member, err := releaseasset.SelectTarGzip(ctx, archive, node.Executable, releaseasset.Limits{
		ExecutableBytes: maxFactoryToolBytes, ExpandedBytes: maxFactoryToolExpandedBytes, Entries: maxFactoryToolEntries,
	})
	if err != nil {
		return err
	}
	if pipeline.FileDigest(member) != node.ExecutableDigest {
		return e.New(e.CodeVerifyFailed, "tool %s executable does not match %s", node.ID, node.ExecutableDigest)
	}
	if err := writeFactoryFile(ctx, output.Path, member, 0o555); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "acquired %s: archive %s, executable %s\n", node.ID, node.Digest, node.ExecutableDigest)
	return nil
}

type factoryObservation struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Qualification EvidenceTier           `json:"qualification"`
	BuildDigest   string                 `json:"buildDigest"`
	BuildTarget   string                 `json:"buildTarget"`
	Inputs        []factoryObservedInput `json:"inputs"`
}

type factoryObservedInput struct {
	ArtifactID string `json:"artifactId"`
	Kind       string `json:"kind"`
	Digest     string `json:"digest"`
}

func executeObservation(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact) error {
	if plan.hasCompiledDiskTarget(node.BuildTarget, artifacts) {
		return e.New(e.CodeEngineCapability,
			"disk terminal observation %q requires the local Run's held producer result", node.ID)
	}
	return executeObservationWithHeldDisk(ctx, plan, node, artifacts, nil)
}

func executeHeldDiskObservation(ctx context.Context, plan *Plan, node Node,
	artifacts map[string]Artifact, execution *LocalRunResult,
) error {
	if execution == nil || !plan.hasCompiledDiskTarget(node.BuildTarget, artifacts) {
		return e.New(e.CodeEngineCapability,
			"disk terminal observation %q requires an exact held producer result", node.ID)
	}
	return executeObservationWithHeldDisk(ctx, plan, node, artifacts, execution)
}

func executeObservationWithHeldDisk(ctx context.Context, plan *Plan, node Node,
	artifacts map[string]Artifact, execution *LocalRunResult,
) error {
	observation := factoryObservation{SchemaVersion: "nvkr.dev/factory-observation/v2", Qualification: TierStaticUnqualified, BuildTarget: node.BuildTarget}
	var diskObservation DiskPayloadObservation
	var diskResult pipeline.ArtifactResult
	for _, buildPlan := range plan.Builds {
		if buildPlan.Name == node.BuildTarget {
			observation.BuildDigest = buildPlan.BuildDigest
			visible := make(map[string]Artifact, len(node.Inputs))
			for _, id := range node.Inputs {
				visible[id] = artifacts[id]
			}
			if execution != nil {
				var expected LifecycleOutputPlan
				for _, output := range plan.LifecycleOutputs {
					if output.Name == node.BuildTarget && output.Kind == authored.OutputDiskImage {
						expected = output
					}
				}
				if expected.Name == "" {
					return e.New(e.CodePipelineDrift,
						"disk terminal observation %q has no lifecycle output binding", node.ID)
				}
				var err error
				diskObservation, diskResult, err = execution.inspectDiskLifecycleMetadata(ctx, plan, expected)
				if err != nil {
					return err
				}
			}
			for _, component := range buildPlan.Components {
				id := "artifact/" + buildPlan.Name + "/" + component
				input, ok := visible[id]
				if !ok || input.BuildTarget != buildPlan.Name || input.Component != component {
					return e.New(e.CodeVerifyFailed, "terminal output %q omits its exact producer bytes for %q", buildPlan.Name, component)
				}
				if execution != nil && component == diskObservation.Component {
					continue
				}
				if _, err := observeProducerPayload(ctx, plan, input, visible); err != nil {
					return err
				}
			}
		}
	}
	diskResultDigest := ""
	if execution != nil {
		raw, err := canonical.Marshal(diskResult)
		if err != nil {
			return err
		}
		diskResultDigest = pipeline.FileDigest(raw)
	}
	diskPayloadInputs, diskResultInputs := 0, 0
	for _, input := range node.Inputs {
		artifact := artifacts[input]
		digest := ""
		if execution != nil && artifact.Component == diskObservation.Component {
			switch artifact.Kind {
			case authored.OutputDiskImage:
				digest = diskObservation.Digest
				diskPayloadInputs++
			case "producer-result":
				digest = diskResultDigest
				diskResultInputs++
			default:
				return e.New(e.CodePipelineDrift,
					"disk terminal input %q has unexpected kind %q", input, artifact.Kind)
			}
		} else {
			var err error
			digest, err = digestFactoryPath(ctx, artifact.Path)
			if err != nil {
				return e.Wrap(e.CodeVerifyFailed, err, "observing factory input %s", input)
			}
		}
		observation.Inputs = append(observation.Inputs, factoryObservedInput{ArtifactID: input, Kind: artifact.Kind, Digest: digest})
	}
	if execution != nil && (diskPayloadInputs != 1 || diskResultInputs != 1) {
		return e.New(e.CodePipelineDrift,
			"disk terminal observation %q requires one held payload and producer result input", node.ID)
	}
	if observation.BuildDigest == "" {
		return e.New(e.CodePipelineDrift, "terminal node %q has no build digest", node.ID)
	}
	raw, err := canonical.Marshal(observation)
	if err != nil {
		return err
	}
	return writeFactoryFile(ctx, artifacts[node.Outputs[0]].Path, append(raw, '\n'), 0o644)
}

func executePublish(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, stdout io.Writer) error {
	return executePublishWithAdmission(ctx, plan, node, artifacts, stdout, cistore.AdmitPrivatePublication)
}

func executePublishWithAdmission(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, stdout io.Writer,
	admit func(context.Context, string, []string, cistore.Options) error,
) error {
	metadataOnlySigner := PerOutputPublication(plan) && node.Target != "release"
	expectedOutputs := 1
	if metadataOnlySigner {
		expectedOutputs = 2
	}
	expectedInputs := 1
	if _, prepared := preparedEvidenceRoute(plan, node); prepared {
		expectedInputs = 3
	}
	if len(node.Inputs) != expectedInputs || len(node.Outputs) != expectedOutputs {
		return e.New(e.CodePipelineDrift, "publish node %q has unexpected inputs or outputs", node.ID)
	}
	referenceOutput := artifacts[node.Outputs[0]]
	var metadataOutput Artifact
	if metadataOnlySigner {
		var err error
		referenceOutput, err = signNodeOutput(node, artifacts, "published-reference")
		if err != nil {
			return err
		}
		metadataOutput, err = signNodeOutput(node, artifacts, ArtifactSigningMetadata)
		if err != nil {
			return err
		}
	}
	target, err := runtimeTarget(plan, node.Target)
	if err != nil {
		return err
	}
	if target.Kind != TargetOCI {
		return e.New(e.CodeEngineCapability, "factory publication target kind %q has no remote adapter", target.Kind)
	}
	username, password := os.Getenv("NVKR_REGISTRY_USER"), os.Getenv("NVKR_CREDENTIAL")
	if target.CredentialRef != "" && (username == "" || password == "") {
		return e.New(e.CodeTrustPolicyMissing,
			"factory publication target %q requires its configured username and credential", node.Target)
	}
	admissionSource := artifacts[node.Inputs[0]].Path
	admittedControlsRoot := ""
	if route, prepared := preparedEvidenceRoute(plan, node); prepared {
		controls, err := signNodeInput(node, artifacts, ArtifactAttestedEvidenceMetadata)
		if err != nil {
			return err
		}
		inspection, err := delivery.InspectMetadata(ctx, controls.Path)
		if err != nil {
			return err
		}
		if err := admitEvidenceMetadata(plan, route, inspection); err != nil {
			return err
		}
		admissionSource = controls.Path
		admittedControlsRoot = inspection.RootDigest
	}
	// Admit credentials and every metadata-derived destination before consuming
	// the original preparation. Completion and Push both bind this exact root.
	expectedRoot, err := admitGitHubPublication(ctx, target, admissionSource, admit)
	if err != nil {
		return err
	}
	if admittedControlsRoot != "" {
		if expectedRoot != "" && expectedRoot != admittedControlsRoot {
			return e.New(e.CodeVerifyFailed, "attested controls changed between scope and publication privacy admission")
		}
		expectedRoot = admittedControlsRoot
	}
	source, err := completePublicationInput(ctx, plan, node, artifacts, productionOCISigner, expectedRoot)
	if err != nil {
		return err
	}
	if plan.Environment.Signing != nil && plan.Environment.Signing.Mode == SigningModeCosign && len(plan.LifecycleOutputs) > 0 {
		inspect := delivery.Inspect
		if PerOutputPublication(plan) && node.Target == "release" {
			inspect = delivery.InspectMetadata
		}
		inspection, err := inspect(ctx, source)
		if err != nil {
			return err
		}
		if err := delivery.RequireAttestedEvidence(inspection); err != nil {
			return err
		}
		if err := admitDevelopmentMetadata(plan, inspection); err != nil {
			return err
		}
	}
	push := func(ctx context.Context, source, ref string, options delivery.RegistryOptions) (string, error) {
		return delivery.PushWithOptions(ctx, source, ref, delivery.PushOptions{Registry: options, ImageTags: target.ImageTags, ExpectedRootDigest: expectedRoot})
	}
	if PerOutputPublication(plan) && node.Target == "release" {
		push = func(ctx context.Context, source, ref string, options delivery.RegistryOptions) (string, error) {
			return delivery.PushMetadataWithOptions(ctx, source, ref, delivery.PushOptions{Registry: options, ExpectedRootDigest: expectedRoot})
		}
	}
	ref, err := push(ctx, source, target.Ref, delivery.RegistryOptions{
		PlainHTTP: target.PlainHTTP, Anonymous: target.Anonymous, Username: username, Password: password,
	})
	if err != nil {
		return err
	}
	if metadataOnlySigner {
		at := strings.LastIndexByte(ref, '@')
		if at < 0 || ref[:at] != target.Ref {
			return e.New(e.CodeVerifyFailed, "published reference differs from planned metadata handoff")
		}
		if _, err := delivery.ExportMetadataWithExpectedRoot(ctx, source, metadataOutput.Path, ref[at+1:]); err != nil {
			return err
		}
	}
	if err := writeFactoryFile(ctx, referenceOutput.Path, []byte(ref+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Published immutable reference: %s\n", ref)
	return nil
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
	temporary, err := os.CreateTemp(filepath.Dir(path), ".nvkr-factory-*")
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
