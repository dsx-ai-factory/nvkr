package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type fakeDiskProducerResult struct {
	digest   string
	size     int64
	payload  []byte
	writeErr error
	cancel   context.CancelFunc
	closed   bool
	closeErr error
	started  chan struct{}
	release  <-chan struct{}
	onClose  func()
}

func (result *fakeDiskProducerResult) WriteToContext(ctx context.Context, destination io.Writer) (int64, error) {
	if result.closed {
		return 0, io.ErrClosedPipe
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if result.started != nil {
		close(result.started)
	}
	if result.release != nil {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-result.release:
		}
	}
	if result.writeErr != nil {
		return 0, result.writeErr
	}
	written := int64(0)
	for offset := 0; offset < len(result.payload); {
		end := offset + 4
		if end > len(result.payload) {
			end = len(result.payload)
		}
		n, err := destination.Write(result.payload[offset:end])
		written += int64(n)
		offset += n
		if result.cancel != nil {
			result.cancel()
			result.cancel = nil
		}
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (result *fakeDiskProducerResult) Close() error {
	result.closed = true
	if result.onClose != nil {
		result.onClose()
	}
	return result.closeErr
}

func fakeDiskPayload(result *fakeDiskProducerResult) *diskPayload {
	return &diskPayload{digest: result.digest, size: result.size, content: result}
}

func fakeDiskPayloadState(payload *diskPayload) *fakeDiskProducerResult {
	return payload.content.(*fakeDiskProducerResult)
}

func replaceFakeDiskPayload(completion *diskProducerCompletion, payload []byte) {
	held := fakeDiskPayloadState(completion.Payload)
	held.payload = payload
	sum := sha256.Sum256(payload)
	held.digest = "sha256:" + hex.EncodeToString(sum[:])
	held.size = int64(len(payload))
	completion.Payload.digest, completion.Payload.size = held.digest, held.size
	completion.Result.SHA256, completion.Result.Size = held.digest, held.size
}

func testQCOW2HeaderPayload() []byte {
	payload := make([]byte, 512)
	copy(payload[:4], qcow2Magic)
	binary.BigEndian.PutUint32(payload[4:8], 3)
	binary.BigEndian.PutUint32(payload[20:24], 9)
	binary.BigEndian.PutUint64(payload[24:32], 1<<20)
	binary.BigEndian.PutUint32(payload[100:104], qcow2Version3HeaderLen)
	return payload
}

func diskObservationFixture(t *testing.T, encryption string) (*Plan, LifecycleOutputPlan, diskProducerCompletion) {
	t.Helper()
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityRequired)
	component := stack.Catalog.Components["image"]
	component.Disk.Encryption = encryption
	component.BuildCommand = nil
	component.Packer = &catalog.PackerConfig{
		ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest",
		OutputDirectoryVariable: "output_directory",
		Variables:               map[string]string{"image_variant": "standard"},
	}
	stack.Catalog.Components["image"] = component
	stack, err := catalog.ApplyStack(stack.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	payload := testQCOW2HeaderPayload()
	sum := sha256.Sum256(payload)
	held := &fakeDiskProducerResult{
		digest: "sha256:" + hex.EncodeToString(sum[:]), size: int64(len(payload)), payload: payload,
	}
	var producer ir.Node
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Component == "image" {
			producer = node
		}
	}
	return plan, plan.LifecycleOutputs[0], diskProducerCompletion{
		NodeID: "node-image/build-image", PlanDigest: planDigest,
		Result: pipeline.ArtifactResult{
			Component: "image", OutputType: "disk-image", Qualification: pipeline.ArtifactUnqualified,
			Toolchain: producer.Toolchain, BuilderType: ir.BuilderVMImage, EngineClass: ir.BuilderVMImage,
			InputDigest: "sha256:" + strings.Repeat("d", 64), Path: producer.ArtifactPath,
			SHA256: held.digest, Size: held.size,
		},
		Payload: fakeDiskPayload(held),
	}
}

func fakeHeldFileProducerCallback(path string, description pipeline.ArtifactResult,
	captured **heldfile.File,
) heldDiskProducerCallback {
	return func(ctx context.Context, _ ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
		held, err := heldfile.OpenContext(ctx, path, heldfile.Options{
			Label: "fake disk producer output", MaxBytes: 1 << 20,
		})
		if err != nil {
			return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "holding fake disk producer output")
		}
		description.SHA256, description.Size = held.Digest(), held.Size()
		if captured != nil {
			*captured = held
		}
		return &diskPayload{digest: held.Digest(), size: held.Size(), content: held}, &description, nil
	}
}

func localDiskRunResult(t *testing.T, plan *Plan) *LocalRunResult {
	t.Helper()
	execution := diskExecutionFixture(t, plan)
	return &LocalRunResult{
		SchemaVersion: LocalRunSchemaVersion, Qualification: TierLocalExecution,
		PlanDigest: execution.PlanDigest, Nodes: execution.Nodes,
	}
}

func TestPopulateDiskProducerCompletionHoldsPayloadAcrossPathReplacement(t *testing.T) {
	plan, expected, completion := diskObservationFixture(t, "none")
	payloadPath := filepath.Join(t.TempDir(), "node.qcow2")
	original := append([]byte(nil), fakeDiskPayloadState(completion.Payload).payload...)
	if err := os.WriteFile(payloadPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	result := localDiskRunResult(t, plan)
	var held *heldfile.File
	if err := populateHeldDiskProducerCompletion(t.Context(), plan, completion.NodeID, result,
		fakeHeldFileProducerCallback(payloadPath, completion.Result, &held)); err != nil {
		t.Fatal(err)
	}
	if held == nil {
		t.Fatal("producer callback did not return a held payload")
	}
	movedPath := filepath.Join(filepath.Dir(payloadPath), "original.qcow2")
	if err := os.Rename(payloadPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := result.ObserveDiskLifecyclePayload(t.Context(), plan, expected)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Digest != held.Digest() || observation.Size != int64(len(original)) {
		t.Fatalf("replacement changed observation: %+v", observation)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := held.WriteToContext(t.Context(), io.Discard); err == nil {
		t.Fatal("held payload remained readable after execution result Close")
	}
}

func TestPopulateDiskProducerCompletionRejectsNonRegularPaths(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(*testing.T) string
	}{
		{name: "directory", path: func(t *testing.T) string { return t.TempDir() }},
		{name: "symlink", path: func(t *testing.T) string {
			directory := t.TempDir()
			target := filepath.Join(directory, "target.qcow2")
			if err := os.WriteFile(target, []byte{'Q', 'F', 'I', 0xfb}, 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(directory, "link.qcow2")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, _, completion := diskObservationFixture(t, "none")
			result := localDiskRunResult(t, plan)
			err := populateHeldDiskProducerCompletion(t.Context(), plan, completion.NodeID, result,
				fakeHeldFileProducerCallback(test.path(t), completion.Result, nil))
			if factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("error = %v, want %s", err, e.CodeVerifyFailed)
			}
			if len(result.diskCompletions) != 0 {
				t.Fatalf("rejected %s recorded a disk completion", test.name)
			}
		})
	}
}

func TestPopulateDiskProducerCompletionCleansRejectedPayloads(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, *diskPayload, pipeline.ArtifactResult) (*diskPayload, *pipeline.ArtifactResult, error)
		code e.Code
	}{
		{name: "callback error", code: e.CodeBuildFailed,
			run: func(_ context.Context, payload *diskPayload, _ pipeline.ArtifactResult) (*diskPayload, *pipeline.ArtifactResult, error) {
				fakeDiskPayloadState(payload).closeErr = errors.New("close sentinel")
				return payload, nil, e.New(e.CodeBuildFailed, "producer sentinel")
			}},
		{name: "missing description", code: e.CodeVerifyFailed,
			run: func(_ context.Context, payload *diskPayload, _ pipeline.ArtifactResult) (*diskPayload, *pipeline.ArtifactResult, error) {
				return payload, nil, nil
			}},
		{name: "canceled after callback", code: e.CodeTimeout,
			run: func(ctx context.Context, payload *diskPayload, description pipeline.ArtifactResult) (*diskPayload, *pipeline.ArtifactResult, error) {
				contextCancel(ctx)
				return payload, &description, nil
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, _, completion := diskObservationFixture(t, "none")
			held := fakeDiskPayloadState(completion.Payload)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.name == "canceled after callback" {
				ctx = context.WithValue(ctx, diskCancelKey{}, cancel)
			}
			err := populateHeldDiskProducerCompletion(ctx, plan, completion.NodeID, localDiskRunResult(t, plan),
				func(ctx context.Context, _ ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
					return test.run(ctx, completion.Payload, completion.Result)
				})
			if factoryErrorCode(err) != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
			if !held.closed {
				t.Fatal("rejected producer payload was not closed")
			}
			if test.name == "callback error" &&
				(!strings.Contains(err.Error(), "producer sentinel") || !strings.Contains(err.Error(), "close sentinel")) {
				t.Fatalf("joined callback/cleanup error = %v", err)
			}
		})
	}
}

type diskCancelKey struct{}

func contextCancel(ctx context.Context) {
	if cancel, ok := ctx.Value(diskCancelKey{}).(context.CancelFunc); ok {
		cancel()
	}
}

func TestPopulateDiskProducerCompletionCancellationAndDuplicate(t *testing.T) {
	plan, _, completion := diskObservationFixture(t, "none")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := populateDiskProducerCompletion(ctx, plan, completion.NodeID, localDiskRunResult(t, plan),
		func(context.Context, Node, ir.Node) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
			called = true
			return nil, nil, nil
		})
	if factoryErrorCode(err) != e.CodeTimeout || called {
		t.Fatalf("pre-canceled population: error=%v called=%v", err, called)
	}

	result := localDiskRunResult(t, plan)
	first := fakeDiskPayloadState(completion.Payload)
	if err := populateHeldDiskProducerCompletion(t.Context(), plan, completion.NodeID, result,
		func(context.Context, ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
			return completion.Payload, &completion.Result, nil
		}); err != nil {
		t.Fatal(err)
	}
	second := *first
	second.closed = false
	if err := populateHeldDiskProducerCompletion(t.Context(), plan, completion.NodeID, result,
		func(context.Context, ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
			return fakeDiskPayload(&second), &completion.Result, nil
		}); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("duplicate error = %v, want %s", err, e.CodeVerifyFailed)
	}
	if !second.closed || first.closed {
		t.Fatalf("duplicate cleanup closed states: first=%v second=%v", first.closed, second.closed)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPopulateDiskProducerCompletionDefensivelyClonesInstruction(t *testing.T) {
	plan, _, completion := diskObservationFixture(t, "none")
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	err = populateDiskProducerCompletion(t.Context(), plan, completion.NodeID, localDiskRunResult(t, plan),
		func(_ context.Context, _ Node, instruction ir.Node) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
			instruction.Needs[0] = "changed"
			instruction.Requirements.Resources.CPUMillis++
			instruction.ExecutionBinding.Class.Name = "changed"
			instruction.Disk.Encryption = "changed"
			instruction.Packer.BuildName = "changed"
			instruction.Packer.Variables[0].Value = "changed"
			return nil, nil, e.New(e.CodeBuildFailed, "stop after mutation")
		})
	if factoryErrorCode(err) != e.CodeBuildFailed {
		t.Fatalf("callback error = %v, want %s", err, e.CodeBuildFailed)
	}
	after, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("producer callback mutated Plan: before=%s after=%s", before, after)
	}
}

func TestRunLocalNodeRoutesDirectPackerThroughConcreteCallback(t *testing.T) {
	plan, _, completion := diskObservationFixture(t, "none")
	var producerNode Node
	for _, node := range plan.Graph.Nodes {
		if node.ID == completion.NodeID {
			producerNode = node
		}
	}
	if producerNode.ID == "" || !requiresDirectPackerDiskAdapter(plan) {
		t.Fatal("fixture has no direct-Packer scheduler node")
	}
	owner := localDiskRunResult(t, plan)
	ordinaryCalled, diskCalled := false, false
	result, err := runLocalNode(t.Context(), plan, producerNode, withLocalDefaults(LocalRunOptions{}), nil, owner,
		localRunDeps{
			executeNode: func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error {
				ordinaryCalled = true
				return nil
			},
			executeDisk: func(_ context.Context, selected Node, instruction ir.Node) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
				diskCalled = true
				if selected.ID != completion.NodeID || instruction.ID != "build-image" || instruction.Packer == nil {
					t.Fatalf("disk callback node/instruction = %#v/%#v", selected, instruction)
				}
				return nil, nil, e.New(e.CodeBuildFailed, "driver unavailable")
			},
		})
	if factoryErrorCode(err) != e.CodeBuildFailed || result.Status != "" || ordinaryCalled || !diskCalled ||
		len(owner.diskCompletions) != 0 {
		t.Fatalf("direct-Packer routing: error=%v ordinary=%v disk=%v result=%+v completions=%d",
			err, ordinaryCalled, diskCalled, result, len(owner.diskCompletions))
	}

	owner = localDiskRunResult(t, plan)
	if _, err := runLocalNode(t.Context(), plan, producerNode, withLocalDefaults(LocalRunOptions{}), nil, owner,
		localRunDeps{executeNode: func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error {
			return nil
		}}); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("missing adapter error = %v, want %s", err, e.CodeEngineCapability)
	}
}

func diskExecutionFixture(t *testing.T, plan *Plan) diskExecutionState {
	t.Helper()
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return diskExecutionState{PlanDigest: digest, Nodes: []LocalNodeResult{{
		ID: "node-image/build-image", Kind: NodeBuild, Status: "ok",
	}}}
}

func TestObserveDiskLifecyclePayloadBindsExactPlanAndProducer(t *testing.T) {
	plan, expected, result := diskObservationFixture(t, "none")
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage || node.Kind == NodeSBOM {
			t.Fatalf("direct-Packer disk gained unsupported package or SBOM work: %#v", node)
		}
	}
	observation, err := observeDiskLifecyclePayload(t.Context(), plan, expected,
		diskExecutionFixture(t, plan), []diskProducerCompletion{result})
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if observation.Digest != result.Payload.digest || observation.Size != result.Payload.size ||
		observation.BuildDigest != expected.BuildDigest ||
		observation.Producer != "build-image" || observation.Component != "image" ||
		observation.Format != "qcow2" || observation.Encryption != "none" ||
		observation.Outcome != "succeeded" || observation.PlanDigest != planDigest ||
		len(observation.Components) != 1 || observation.Components[0] != "image" {
		t.Fatalf("disk observation = %+v", observation)
	}
}

func TestObserveDiskLifecyclePayloadRejectsSubstitutionAndAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Plan, *LifecycleOutputPlan, *diskExecutionState, *diskProducerCompletion, *[]diskProducerCompletion)
		code   e.Code
	}{
		{name: "wrong producer outcome", change: func(_ *Plan, _ *LifecycleOutputPlan, execution *diskExecutionState, _ *diskProducerCompletion, _ *[]diskProducerCompletion) {
			execution.Nodes[0].Status = "failed"
		}, code: e.CodeVerifyFailed},
		{name: "wrong producer", change: func(_ *Plan, _ *LifecycleOutputPlan, execution *diskExecutionState, _ *diskProducerCompletion, _ *[]diskProducerCompletion) {
			execution.Nodes[0].ID = "node-image/build-other"
		}, code: e.CodeVerifyFailed},
		{name: "duplicate producer outcome", change: func(_ *Plan, _ *LifecycleOutputPlan, execution *diskExecutionState, _ *diskProducerCompletion, _ *[]diskProducerCompletion) {
			execution.Nodes = append(execution.Nodes, execution.Nodes[0])
		}, code: e.CodeVerifyFailed},
		{name: "wrong completion producer", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.NodeID = "other/build-image"
		}, code: e.CodeReleaseIncomplete},
		{name: "wrong component", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.Result.Component = "other"
		}, code: e.CodeVerifyFailed},
		{name: "wrong output type", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.Result.OutputType = "raw"
		}, code: e.CodeVerifyFailed},
		{name: "wrong builder", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.Result.BuilderType = "oci-hermetic"
		}, code: e.CodeVerifyFailed},
		{name: "wrong artifact path", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.Result.Path = "other.qcow2"
		}, code: e.CodeVerifyFailed},
		{name: "caller payload path", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.Result.AbsPath = "/caller/node.qcow2"
		}, code: e.CodeVerifyFailed},
		{name: "caller transport path", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.Result.TransportPath = "/caller/node.qcow2"
		}, code: e.CodeVerifyFailed},
		{name: "wrong digest", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.Result.SHA256 = "sha256:" + strings.Repeat("a", 64)
		}, code: e.CodeVerifyFailed},
		{name: "wrong size", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.Result.Size++
		}, code: e.CodeVerifyFailed},
		{name: "payload changed", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			fakeDiskPayloadState(result.Payload).payload[4] ^= 0xff
		}, code: e.CodeVerifyFailed},
		{name: "mutable content identity cannot retarget snapshot", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			held := fakeDiskPayloadState(result.Payload)
			held.payload = append([]byte(nil), held.payload...)
			held.payload[len(held.payload)-1] ^= 0xff
			sum := sha256.Sum256(held.payload)
			held.digest = "sha256:" + hex.EncodeToString(sum[:])
			held.size = int64(len(held.payload))
			result.Result.SHA256, result.Result.Size = held.digest, held.size
		}, code: e.CodeVerifyFailed},
		{name: "not qcow2 bytes", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			replaceFakeDiskPayload(result, []byte("not-qcow2"))
		}, code: e.CodeVerifyFailed},
		{name: "unsupported qcow2 version", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint32(payload[4:8], 4)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "qcow2 backing offset", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint64(payload[8:16], 4096)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "qcow2 backing size", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint32(payload[16:20], 64)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "invalid qcow2 cluster bits", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint32(payload[20:24], 8)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "qcow2 cluster bits above admitted range", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint32(payload[20:24], 22)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "zero qcow2 virtual size", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint64(payload[24:32], 0)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "qcow2 container encryption", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint32(payload[32:36], 1)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "qcow2 v3 header length below minimum", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint32(payload[100:104], qcow2Version3HeaderLen-1)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "qcow2 v3 header length is not aligned", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()
			binary.BigEndian.PutUint32(payload[100:104], qcow2Version3HeaderLen+1)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "qcow2 v3 header length beyond cluster", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := append(testQCOW2HeaderPayload(), make([]byte, 512)...)
			binary.BigEndian.PutUint32(payload[100:104], 513)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "qcow2 v3 header length beyond payload", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			payload := testQCOW2HeaderPayload()[:qcow2Version3HeaderLen]
			binary.BigEndian.PutUint32(payload[100:104], 512)
			replaceFakeDiskPayload(result, payload)
		}, code: e.CodeVerifyFailed},
		{name: "truncated qcow2 header", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			replaceFakeDiskPayload(result, testQCOW2HeaderPayload()[:64])
		}, code: e.CodeVerifyFailed},
		{name: "wrong build", change: func(_ *Plan, expected *LifecycleOutputPlan, _ *diskExecutionState, _ *diskProducerCompletion, _ *[]diskProducerCompletion) {
			expected.BuildDigest = "sha256:" + strings.Repeat("b", 64)
		}, code: e.CodeVerifyFailed},
		{name: "wrong plan outcome", change: func(_ *Plan, _ *LifecycleOutputPlan, execution *diskExecutionState, _ *diskProducerCompletion, _ *[]diskProducerCompletion) {
			execution.PlanDigest = "sha256:" + strings.Repeat("b", 64)
		}, code: e.CodeVerifyFailed},
		{name: "private producer Plan binding tamper", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, _ *[]diskProducerCompletion) {
			result.PlanDigest = "sha256:" + strings.Repeat("b", 64)
		}, code: e.CodeVerifyFailed},
		{name: "public Plan fields cannot retarget private producer completion", change: func(plan *Plan, _ *LifecycleOutputPlan, execution *diskExecutionState, _ *diskProducerCompletion, _ *[]diskProducerCompletion) {
			plan.GeneratorVersion += "-other"
			digest, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			execution.PlanDigest = digest
		}, code: e.CodeVerifyFailed},
		{name: "wrong disk format", change: func(plan *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, _ *diskProducerCompletion, _ *[]diskProducerCompletion) {
			mutateFixtureDiskProducer(t, plan, func(node *ir.Node) { node.Disk.Format = "raw" })
		}, code: e.CodeInvalidCatalog},
		{name: "wrong disk encryption", change: func(plan *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, _ *diskProducerCompletion, _ *[]diskProducerCompletion) {
			mutateFixtureDiskProducer(t, plan, func(node *ir.Node) { node.Disk.Encryption = "unknown" })
		}, code: e.CodeInvalidCatalog},
		{name: "duplicate result", change: func(_ *Plan, _ *LifecycleOutputPlan, _ *diskExecutionState, result *diskProducerCompletion, results *[]diskProducerCompletion) {
			*results = append(*results, *result)
		}, code: e.CodeReleaseIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, expected, result := diskObservationFixture(t, "none")
			execution := diskExecutionFixture(t, plan)
			results := []diskProducerCompletion{result}
			tc.change(plan, &expected, &execution, &result, &results)
			results[0] = result
			if _, err := observeDiskLifecyclePayload(t.Context(), plan, expected, execution, results); factoryErrorCode(err) != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
}

func mutateFixtureDiskProducer(t *testing.T, plan *Plan, mutate func(*ir.Node)) {
	t.Helper()
	for index := range plan.Builds[0].Graph.Nodes {
		node := &plan.Builds[0].Graph.Nodes[index]
		if node.Component == "image" && node.Disk != nil {
			mutate(node)
			return
		}
	}
	t.Fatal("fixture has no disk producer")
}

func TestObserveDiskLifecyclePayloadHonorsCancellation(t *testing.T) {
	plan, expected, result := diskObservationFixture(t, "none")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeDiskLifecyclePayload(ctx, plan, expected,
		diskExecutionFixture(t, plan), []diskProducerCompletion{result}); factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("error = %v, want %s", err, e.CodeTimeout)
	}
}

func TestObserveDiskLifecyclePayloadHonorsMidStreamCancellation(t *testing.T) {
	plan, expected, result := diskObservationFixture(t, "none")
	ctx, cancel := context.WithCancel(context.Background())
	fakeDiskPayloadState(result.Payload).cancel = cancel
	if _, err := observeDiskLifecyclePayload(ctx, plan, expected,
		diskExecutionFixture(t, plan), []diskProducerCompletion{result}); factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("error = %v, want %s", err, e.CodeTimeout)
	}
}

func TestObserveDiskLifecyclePayloadDistinguishesPlainAndLUKS2(t *testing.T) {
	plainPlan, plainExpected, plainResult := diskObservationFixture(t, "none")
	plain, err := observeDiskLifecyclePayload(t.Context(), plainPlan, plainExpected,
		diskExecutionFixture(t, plainPlan), []diskProducerCompletion{plainResult})
	if err != nil {
		t.Fatal(err)
	}
	luksPlan, luksExpected, luksResult := diskObservationFixture(t, "luks2")
	luks, err := observeDiskLifecyclePayload(t.Context(), luksPlan, luksExpected,
		diskExecutionFixture(t, luksPlan), []diskProducerCompletion{luksResult})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Encryption == luks.Encryption || plain.PlanDigest == luks.PlanDigest {
		t.Fatalf("plain and LUKS2 observations are not identity-distinct: plain=%+v luks=%+v", plain, luks)
	}
}

func TestLocalRunResultOwnsDiskPayloadThroughObservation(t *testing.T) {
	plan, expected, completion := diskObservationFixture(t, "none")
	execution := diskExecutionFixture(t, plan)
	result := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion, Qualification: TierLocalExecution,
		PlanDigest: execution.PlanDigest, Nodes: execution.Nodes}
	if err := result.recordDiskCompletion(completion); err != nil {
		t.Fatal(err)
	}
	if err := result.recordDiskCompletion(completion); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("duplicate error = %v, want %s", err, e.CodeVerifyFailed)
	}
	if _, err := result.ObserveDiskLifecyclePayload(t.Context(), plan, expected); err != nil {
		t.Fatal(err)
	}
	result.SchemaVersion = "forged"
	if _, err := result.ObserveDiskLifecyclePayload(t.Context(), plan, expected); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("forged execution error = %v, want %s", err, e.CodeVerifyFailed)
	}
	result.SchemaVersion = LocalRunSchemaVersion
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	if !fakeDiskPayloadState(completion.Payload).closed {
		t.Fatal("local Run result did not close its held disk payload")
	}
	if _, err := result.ObserveDiskLifecyclePayload(t.Context(), plan, expected); factoryErrorCode(err) != e.CodeGateUnmet {
		t.Fatalf("closed observation error = %v, want %s", err, e.CodeGateUnmet)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
}

func TestLocalRunResultCloseReportsHeldPayloadFailure(t *testing.T) {
	_, _, completion := diskObservationFixture(t, "none")
	fakeDiskPayloadState(completion.Payload).closeErr = io.ErrUnexpectedEOF
	result := &LocalRunResult{}
	if err := result.recordDiskCompletion(completion); err != nil {
		t.Fatal(err)
	}
	if err := result.Close(); factoryErrorCode(err) != e.CodeInternal {
		t.Fatalf("close error = %v, want %s", err, e.CodeInternal)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
}

func TestLocalRunResultCloseJoinsAllHeldPayloadFailures(t *testing.T) {
	first := &fakeDiskProducerResult{closeErr: errors.New("first close sentinel")}
	second := &fakeDiskProducerResult{closeErr: errors.New("second close sentinel")}
	result := &LocalRunResult{}
	if err := result.recordDiskCompletion(diskProducerCompletion{NodeID: "first", Payload: fakeDiskPayload(first)}); err != nil {
		t.Fatal(err)
	}
	if err := result.recordDiskCompletion(diskProducerCompletion{NodeID: "second", Payload: fakeDiskPayload(second)}); err != nil {
		t.Fatal(err)
	}
	err := result.Close()
	if factoryErrorCode(err) != e.CodeInternal ||
		!strings.Contains(err.Error(), "first close sentinel") ||
		!strings.Contains(err.Error(), "second close sentinel") {
		t.Fatalf("close error = %v", err)
	}
	if !first.closed || !second.closed {
		t.Fatalf("Close did not attempt every payload: first=%v second=%v", first.closed, second.closed)
	}
}

func TestLocalRunResultSerializesObservationAndClose(t *testing.T) {
	plan, expected, completion := diskObservationFixture(t, "none")
	execution := diskExecutionFixture(t, plan)
	started, release := make(chan struct{}), make(chan struct{})
	held := fakeDiskPayloadState(completion.Payload)
	held.started, held.release = started, release
	result := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion, Qualification: TierLocalExecution,
		PlanDigest: execution.PlanDigest, Nodes: execution.Nodes}
	if err := result.recordDiskCompletion(completion); err != nil {
		t.Fatal(err)
	}
	observeDone := make(chan error, 1)
	go func() {
		_, err := result.ObserveDiskLifecyclePayload(t.Context(), plan, expected)
		observeDone <- err
	}()
	<-started
	permit := result.diskOperationPermit()
	select {
	case <-permit:
		permit <- struct{}{}
		t.Fatal("observation did not retain result lifetime lock while streaming")
	default:
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- result.Close() }()
	if held.closed {
		t.Fatal("racing close released payload during observation")
	}
	close(release)
	if err := <-observeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if !held.closed {
		t.Fatal("close did not release payload after observation")
	}
}

func TestLocalRunResultWaitingObservationHonorsCancellation(t *testing.T) {
	plan, expected, completion := diskObservationFixture(t, "none")
	started, release := make(chan struct{}), make(chan struct{})
	held := fakeDiskPayloadState(completion.Payload)
	held.started, held.release = started, release
	result := localDiskRunResult(t, plan)
	if err := result.recordDiskCompletion(completion); err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := result.ObserveDiskLifecyclePayload(t.Context(), plan, expected)
		firstDone <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := result.ObserveDiskLifecyclePayload(ctx, plan, expected)
		secondDone <- err
	}()
	cancel()
	if err := <-secondDone; factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("waiting observation error = %v, want %s", err, e.CodeTimeout)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRunResultStreamFailureUnblocksRacingClose(t *testing.T) {
	plan, expected, completion := diskObservationFixture(t, "none")
	started, release := make(chan struct{}), make(chan struct{})
	held := fakeDiskPayloadState(completion.Payload)
	held.started, held.release, held.writeErr = started, release, errors.New("stream sentinel")
	result := localDiskRunResult(t, plan)
	if err := result.recordDiskCompletion(completion); err != nil {
		t.Fatal(err)
	}
	observeDone := make(chan error, 1)
	go func() {
		_, err := result.ObserveDiskLifecyclePayload(t.Context(), plan, expected)
		observeDone <- err
	}()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- result.Close() }()
	close(release)
	if err := <-observeDone; factoryErrorCode(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), "stream sentinel") {
		t.Fatalf("observation error = %v, want %s with stream cause", err, e.CodeVerifyFailed)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if !held.closed {
		t.Fatal("racing Close did not release payload after stream failure")
	}
}

func TestLocalRunResultCancellationUnblocksRacingClose(t *testing.T) {
	plan, expected, completion := diskObservationFixture(t, "none")
	started := make(chan struct{})
	held := fakeDiskPayloadState(completion.Payload)
	held.started, held.release = started, make(chan struct{})
	result := localDiskRunResult(t, plan)
	if err := result.recordDiskCompletion(completion); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	observeDone := make(chan error, 1)
	go func() {
		_, err := result.ObserveDiskLifecyclePayload(ctx, plan, expected)
		observeDone <- err
	}()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- result.Close() }()
	cancel()
	if err := <-observeDone; factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("observation error = %v, want %s", err, e.CodeTimeout)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if !held.closed {
		t.Fatal("racing Close did not release payload after canceled observation")
	}
	if _, err := result.ObserveDiskLifecyclePayload(t.Context(), plan, expected); factoryErrorCode(err) != e.CodeGateUnmet {
		t.Fatalf("post-close error = %v, want %s", err, e.CodeGateUnmet)
	}
}

func TestLocalRunResultSerializationExcludesHeldCapabilities(t *testing.T) {
	plan, _, completion := diskObservationFixture(t, "none")
	result := localDiskRunResult(t, plan)
	if err := result.recordDiskCompletion(completion); err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Marshal(&LocalRunResult{
		SchemaVersion: result.SchemaVersion, Qualification: result.Qualification,
		PlanDigest: result.PlanDigest, Workspace: result.Workspace, Nodes: result.Nodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) || bytes.Contains(raw, []byte("diskCompletions")) ||
		bytes.Contains(raw, fakeDiskPayloadState(completion.Payload).payload) {
		t.Fatalf("serialized local result leaked held disk authority: %s", raw)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
}
