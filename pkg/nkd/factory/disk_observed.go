package factory

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

const (
	qcow2Magic             = "QFI\xfb"
	qcow2Version2HeaderLen = 72
	qcow2Version3HeaderLen = 104
	qcow2HeaderCaptureLen  = qcow2Version3HeaderLen
)

// diskPayloadContent is held only behind factory's opaque diskPayload. It lets
// controlled tests exercise lifecycle failures without weakening the production
// callback, which accepts pipeline's concrete package-issued PackerArtifact.
// WriteToContext implementations must stop streaming and return when ctx is
// canceled; observation holds the result lifetime permit until that return.
type diskPayloadContent interface {
	WriteToContext(context.Context, io.Writer) (int64, error)
	Close() error
}

// diskPayload is the factory-owned lifetime capability for one immutable
// producer payload. Digest and size are frozen once when the producer transfers
// ownership; later observation never asks mutable content to redefine them.
type diskPayload struct {
	digest  string
	size    int64
	content diskPayloadContent
}

func (payload *diskPayload) WriteToContext(ctx context.Context, destination io.Writer) (int64, error) {
	if payload == nil || payload.content == nil {
		return 0, e.New(e.CodeVerifyFailed, "disk payload capability is incomplete")
	}
	return payload.content.WriteToContext(ctx, destination)
}

func (payload *diskPayload) Close() error {
	if payload == nil || payload.content == nil {
		return nil
	}
	return payload.content.Close()
}

// diskProducerCompletion pairs the held payload with the Packer driver's
// unqualified descriptive result. Semantic and outcome fields are re-derived or checked
// against the Plan and execution state below; none is copied as authority.
type diskProducerCompletion struct {
	NodeID     string
	PlanDigest string
	Result     pipeline.ArtifactResult
	Payload    *diskPayload
}

// diskProducerCallback is the private population seam for the eventual VM
// driver adapter. Its exact IR instruction comes from the validated Plan, and
// the concrete PackerArtifact is pipeline's package-issued held capability. No
// path, digest, structural implementation, or caller-composed completion can
// enter LocalRunResult through this production boundary.
type diskProducerCallback func(context.Context, ir.Node) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error)

// heldDiskProducerCallback is factory's internal adapter after a concrete
// PackerArtifact has transferred ownership. Tests use it with a factory-owned
// fake diskPayload; it is not the production scheduler seam.
type heldDiskProducerCallback func(context.Context, ir.Node) (*diskPayload, *pipeline.ArtifactResult, error)

type diskExecutionState struct {
	PlanDigest string
	Nodes      []LocalNodeResult
}

// DiskPayloadObservation binds one exact delivered QCOW2 to its logical Build
// and Plan identities. Encryption records the producer's none/luks2 declaration,
// not QCOW container encryption, which the bounded framing check rejects. It
// deliberately contains no RAW reproduction, inspection, SBOM, compatibility,
// or publication verdict.
type DiskPayloadObservation struct {
	Digest      string
	Size        int64
	BuildDigest string
	Components  []string
	Producer    string
	Component   string
	Format      string
	Encryption  string
	Outcome     string
	PlanDigest  string
}

func populateDiskProducerCompletion(ctx context.Context, plan *Plan, nodeID string,
	execution *LocalRunResult, run diskProducerCallback,
) error {
	if run == nil {
		return e.New(e.CodeEngineCapability, "disk producer execution adapter is incomplete")
	}
	return populateHeldDiskProducerCompletion(ctx, plan, nodeID, execution,
		func(ctx context.Context, instruction ir.Node) (*diskPayload, *pipeline.ArtifactResult, error) {
			artifact, description, err := run(ctx, instruction)
			payload := holdPackerDiskPayload(artifact)
			if err == nil && payload != nil &&
				(!canonical.IsDigest(payload.digest) || payload.size <= 0) {
				err = e.New(e.CodeVerifyFailed,
					"Packer returned an invalid held disk payload identity")
			}
			return payload, description, err
		})
}

func holdPackerDiskPayload(artifact *pipeline.PackerArtifact) *diskPayload {
	if artifact == nil {
		return nil
	}
	return &diskPayload{digest: artifact.Digest(), size: artifact.Size(), content: artifact}
}

func populateHeldDiskProducerCompletion(ctx context.Context, plan *Plan, nodeID string,
	execution *LocalRunResult, run heldDiskProducerCallback,
) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "disk producer execution requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "disk producer execution canceled")
	}
	if plan == nil || execution == nil || run == nil {
		return e.New(e.CodeEngineCapability, "disk producer execution adapter is incomplete")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return err
	}
	var selected Node
	matches := 0
	for _, candidate := range plan.Graph.Nodes {
		if candidate.ID == nodeID {
			selected = candidate
			matches++
		}
	}
	if matches != 1 || selected.Kind != NodeBuild || selected.IRNode == "" {
		return e.New(e.CodeEngineCapability,
			"Plan node %q is not a unique disk producer operation", nodeID)
	}
	_, instruction, err := plan.producerInstruction(selected)
	if err != nil {
		return err
	}
	if instruction.Kind != ir.KindBuild || instruction.BuilderType != ir.BuilderVMImage ||
		instruction.OutputType != authored.OutputDiskImage || instruction.Disk == nil ||
		instruction.Disk.Format != "qcow2" || instruction.Packer == nil {
		return e.New(e.CodeEngineCapability,
			"Plan node %q has no admitted direct-Packer QCOW2 producer", nodeID)
	}
	payload, description, runErr := run(ctx, cloneDiskProducerInstruction(instruction))
	if runErr != nil {
		return closeRejectedDiskPayload(runErr, payload)
	}
	if err := ctx.Err(); err != nil {
		return closeRejectedDiskPayload(e.Wrap(e.CodeTimeout, err,
			"disk producer execution canceled"), payload)
	}
	if payload == nil || description == nil {
		return closeRejectedDiskPayload(e.New(e.CodeVerifyFailed,
			"disk producer returned no held payload and descriptive result"), payload)
	}
	completion := diskProducerCompletion{
		NodeID: selected.ID, PlanDigest: planDigest, Result: *description, Payload: payload,
	}
	if err := execution.recordDiskCompletion(completion); err != nil {
		return closeRejectedDiskPayload(err, payload)
	}
	return nil
}

func cloneDiskProducerInstruction(source ir.Node) ir.Node {
	clone := source
	clone.Needs = append([]string(nil), source.Needs...)
	clone.Requirements = execution.Clone(source.Requirements)
	clone.ExecutionBinding = execution.CloneBinding(source.ExecutionBinding)
	clone.VendorCmd = append([]string(nil), source.VendorCmd...)
	clone.Env = append([]ir.EnvVar(nil), source.Env...)
	clone.Patches = append([]ir.PatchRef(nil), source.Patches...)
	clone.Command = append([]string(nil), source.Command...)
	clone.Materials = append([]ir.MaterialBinding(nil), source.Materials...)
	if source.Disk != nil {
		disk := *source.Disk
		clone.Disk = &disk
	}
	if source.Packer != nil {
		packer := *source.Packer
		packer.Variables = append([]ir.PackerVariable(nil), source.Packer.Variables...)
		clone.Packer = &packer
	}
	return clone
}

func closeRejectedDiskPayload(cause error, payload *diskPayload) error {
	if payload == nil {
		return cause
	}
	closeErr := payload.Close()
	if closeErr == nil {
		return cause
	}
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(cause, &coded) {
		code = coded.Code
	}
	return e.Wrap(code, errors.Join(cause, closeErr),
		"rejecting disk producer result and closing its held payload")
}

// observeDiskLifecyclePayload checks one and only one authenticated producer
// result against the compiler-owned disk root, then independently streams and
// hashes the held bytes. Large disks are never loaded into memory.
func observeDiskLifecyclePayload(ctx context.Context, plan *Plan, expected LifecycleOutputPlan,
	execution diskExecutionState, results []diskProducerCompletion,
) (DiskPayloadObservation, error) {
	if ctx == nil {
		return DiskPayloadObservation{}, e.New(e.CodeUsage, "disk lifecycle observation requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return DiskPayloadObservation{}, e.Wrap(e.CodeTimeout, err, "disk lifecycle observation canceled")
	}
	if plan == nil {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed, "disk lifecycle observation requires its compiled plan")
	}
	if err := plan.Validate(); err != nil {
		return DiskPayloadObservation{}, err
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return DiskPayloadObservation{}, err
	}
	if execution.PlanDigest != planDigest {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle output %q execution does not bind its exact Plan", expected.Name)
	}
	if expected.Kind != authored.OutputDiskImage || !canonical.IsDigest(expected.BuildDigest) {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle output %q has no exact disk build binding", expected.Name)
	}
	bindings := 0
	for _, candidate := range plan.LifecycleOutputs {
		if candidate.Name == expected.Name && candidate.Kind == expected.Kind &&
			candidate.ArtifactID == expected.ArtifactID && candidate.BuildDigest == expected.BuildDigest &&
			equalFactoryStrings(candidate.Components, expected.Components) {
			bindings++
		}
	}
	if bindings != 1 {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle output %q differs from its unique plan binding", expected.Name)
	}
	var build *BuildPlan
	for index := range plan.Builds {
		candidate := &plan.Builds[index]
		if candidate.Name == expected.Name && candidate.BuildDigest == expected.BuildDigest {
			if build != nil {
				return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
					"disk lifecycle output %q has an ambiguous logical build", expected.Name)
			}
			build = candidate
		}
	}
	if build == nil || build.Graph == nil {
		return DiskPayloadObservation{}, e.New(e.CodeReleaseIncomplete,
			"disk lifecycle output %q has no exact logical build", expected.Name)
	}
	root, err := buildgraph.DiskRootName(build.ResolvedComponents)
	if err != nil {
		return DiskPayloadObservation{}, e.Wrap(e.CodeVerifyFailed, err,
			"disk lifecycle output %q has no unique QCOW2 root", expected.Name)
	}
	var producer ir.Node
	producers := 0
	for _, node := range build.Graph.Nodes {
		if node.Component == root && node.Kind == ir.KindBuild && node.BuilderType == ir.BuilderVMImage &&
			node.OutputType == authored.OutputDiskImage && node.Disk != nil {
			producer = node
			producers++
		}
	}
	if producers != 1 || producer.Disk.Format != "qcow2" ||
		(producer.Disk.Encryption != "none" && producer.Disk.Encryption != "luks2") {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle output %q has no unique admitted QCOW2 producer", expected.Name)
	}
	producerNodeID := expected.Name + "/" + producer.ID
	outcomes, successful := 0, 0
	for _, node := range execution.Nodes {
		if node.ID == producerNodeID {
			outcomes++
			if node.Kind == NodeBuild && node.Status == "ok" {
				successful++
			}
		}
	}
	if outcomes != 1 || successful != 1 {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle output %q has no unique successful producer outcome", expected.Name)
	}
	var completion diskProducerCompletion
	completions := 0
	for _, candidate := range results {
		if candidate.NodeID == producerNodeID {
			completion = candidate
			completions++
		}
	}
	if completions != 1 || completion.Payload == nil {
		return DiskPayloadObservation{}, e.New(e.CodeReleaseIncomplete,
			"disk lifecycle output %q requires exactly one authenticated producer result", expected.Name)
	}
	if completion.PlanDigest != planDigest {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle output %q producer did not execute under its exact Plan", expected.Name)
	}
	result, payload := completion.Result, completion.Payload
	if result.Component != root || result.OutputType != authored.OutputDiskImage ||
		result.Qualification != pipeline.ArtifactUnqualified || result.Toolchain != producer.Toolchain ||
		result.BuilderType != producer.BuilderType || result.EngineClass != producer.BuilderType ||
		result.Path != producer.ArtifactPath || result.AbsPath != "" || result.TransportPath != "" ||
		result.Ref != "" || result.Packaged ||
		result.ProfileDigest != "" || result.ExecutionBinding != "" ||
		!canonical.IsDigest(result.InputDigest) || !canonical.IsDigest(result.SHA256) || result.Size <= 0 ||
		!canonical.IsDigest(payload.digest) || payload.size <= 0 ||
		result.SHA256 != payload.digest || result.Size != payload.size {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle output %q producer result differs from its compiled producer", expected.Name)
	}
	stream := &diskObservationWriter{ctx: ctx, hash: sha256.New()}
	written, err := payload.WriteToContext(ctx, stream)
	if err != nil {
		if ctx.Err() != nil {
			return DiskPayloadObservation{}, e.Wrap(e.CodeTimeout, ctx.Err(), "observing disk lifecycle payload")
		}
		return DiskPayloadObservation{}, e.Wrap(e.CodeVerifyFailed, err, "observing disk lifecycle payload")
	}
	digest := "sha256:" + hex.EncodeToString(stream.hash.Sum(nil))
	if written != stream.size || stream.size != payload.size || digest != payload.digest {
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle output %q bytes differ from producer result: got %s/%d, want %s/%d",
			expected.Name, digest, stream.size, payload.digest, payload.size)
	}
	if err := validateQCOW2Header(stream.header, stream.size); err != nil {
		return DiskPayloadObservation{}, e.Wrap(e.CodeVerifyFailed, err,
			"disk lifecycle output %q does not carry an admitted QCOW2 header", expected.Name)
	}
	return DiskPayloadObservation{
		Digest: digest, Size: stream.size, BuildDigest: build.BuildDigest,
		Components: append([]string(nil), expected.Components...),
		Producer:   producer.ID, Component: root, Format: producer.Disk.Format,
		Encryption: producer.Disk.Encryption, Outcome: "succeeded", PlanDigest: planDigest,
	}, nil
}

// ObserveDiskLifecyclePayload consumes one still-open local execution result and
// independently rechecks its disk payload against the exact compiled Plan. The
// result owns the held payload until Close; observing and closing are serialized.
func (result *LocalRunResult) ObserveDiskLifecyclePayload(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan,
) (DiskPayloadObservation, error) {
	if result == nil {
		return DiskPayloadObservation{}, e.New(e.CodeReleaseIncomplete,
			"disk lifecycle observation requires a local execution result")
	}
	if ctx == nil {
		return DiskPayloadObservation{}, e.New(e.CodeUsage,
			"disk lifecycle observation requires a non-nil context")
	}
	release, err := result.acquireDiskPermit(ctx)
	if err != nil {
		return DiskPayloadObservation{}, err
	}
	defer release()
	result.diskMu.Lock()
	if result.diskClosed {
		result.diskMu.Unlock()
		return DiskPayloadObservation{}, e.New(e.CodeGateUnmet,
			"local disk execution result is closed")
	}
	if result.SchemaVersion != LocalRunSchemaVersion || result.Qualification != TierLocalExecution {
		result.diskMu.Unlock()
		return DiskPayloadObservation{}, e.New(e.CodeVerifyFailed,
			"disk lifecycle observation requires an exact local execution record")
	}
	execution := diskExecutionState{
		PlanDigest: result.PlanDigest, Nodes: append([]LocalNodeResult(nil), result.Nodes...),
	}
	completions := append([]diskProducerCompletion(nil), result.diskCompletions...)
	result.diskMu.Unlock()
	return observeDiskLifecyclePayload(ctx, plan, expected, execution, completions)
}

// Close releases every held disk payload owned by this local execution result.
// It is idempotent and races safely with observation.
func (result *LocalRunResult) Close() error {
	if result == nil {
		return nil
	}
	permit := result.diskOperationPermit()
	<-permit
	defer func() { permit <- struct{}{} }()
	result.diskMu.Lock()
	if result.diskClosed {
		result.diskMu.Unlock()
		return nil
	}
	result.diskClosed = true
	completions := result.diskCompletions
	result.diskCompletions = nil
	result.diskMu.Unlock()
	var closeErr error
	for _, completion := range completions {
		if completion.Payload != nil {
			closeErr = errors.Join(closeErr, completion.Payload.Close())
		}
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing local disk execution results")
	}
	return nil
}

func (result *LocalRunResult) diskOperationPermit() chan struct{} {
	result.diskMu.Lock()
	defer result.diskMu.Unlock()
	if result.diskPermit == nil {
		result.diskPermit = make(chan struct{}, 1)
		result.diskPermit <- struct{}{}
	}
	return result.diskPermit
}

func (result *LocalRunResult) acquireDiskPermit(ctx context.Context) (func(), error) {
	permit := result.diskOperationPermit()
	select {
	case <-ctx.Done():
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "waiting to observe disk lifecycle payload")
	case <-permit:
		return func() { permit <- struct{}{} }, nil
	}
}

func (result *LocalRunResult) recordDiskCompletion(completion diskProducerCompletion) error {
	if result == nil || completion.NodeID == "" || completion.Payload == nil {
		return e.New(e.CodeInternal, "local disk execution returned an incomplete producer capability")
	}
	result.diskMu.Lock()
	defer result.diskMu.Unlock()
	if result.diskClosed {
		return e.New(e.CodeGateUnmet, "local disk execution result is closed")
	}
	for _, recorded := range result.diskCompletions {
		if recorded.NodeID == completion.NodeID {
			return e.New(e.CodeVerifyFailed,
				"local disk producer %q returned more than one payload", completion.NodeID)
		}
	}
	result.diskCompletions = append(result.diskCompletions, completion)
	return nil
}

type diskObservationWriter struct {
	ctx    context.Context
	hash   hash.Hash
	header []byte
	size   int64
}

// validateQCOW2Header performs a bounded framing check, not a complete QCOW2
// decoder or compatibility verdict. Full format and decoded-RAW verification
// remain owned by the disk verification adapter.
func validateQCOW2Header(header []byte, payloadSize int64) error {
	if len(header) < 8 || string(header[:4]) != qcow2Magic {
		return errors.New("QCOW2 magic is absent")
	}
	version := binary.BigEndian.Uint32(header[4:8])
	minimum := qcow2Version2HeaderLen
	switch version {
	case 2:
	case 3:
		minimum = qcow2Version3HeaderLen
	default:
		return errors.New("QCOW2 version is unsupported")
	}
	if len(header) < minimum || payloadSize < int64(minimum) {
		return errors.New("QCOW2 header is truncated")
	}
	if binary.BigEndian.Uint64(header[8:16]) != 0 || binary.BigEndian.Uint32(header[16:20]) != 0 {
		return errors.New("QCOW2 backing files are unsupported")
	}
	clusterBits := binary.BigEndian.Uint32(header[20:24])
	if clusterBits < 9 || clusterBits > 21 {
		return errors.New("QCOW2 cluster size is outside the admitted range")
	}
	if binary.BigEndian.Uint64(header[24:32]) == 0 {
		return errors.New("QCOW2 virtual size is zero")
	}
	if binary.BigEndian.Uint32(header[32:36]) != 0 {
		return errors.New("QCOW2 container encryption is unsupported")
	}
	if version == 3 {
		headerLength := binary.BigEndian.Uint32(header[100:104])
		clusterSize := uint32(1) << clusterBits
		if headerLength < qcow2Version3HeaderLen || headerLength%8 != 0 ||
			headerLength > clusterSize || int64(headerLength) > payloadSize {
			return errors.New("QCOW2 v3 header length is invalid")
		}
	}
	return nil
}

func (writer *diskObservationWriter) Write(chunk []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	if len(writer.header) < qcow2HeaderCaptureLen {
		remaining := qcow2HeaderCaptureLen - len(writer.header)
		if remaining > len(chunk) {
			remaining = len(chunk)
		}
		writer.header = append(writer.header, chunk[:remaining]...)
	}
	n, err := writer.hash.Write(chunk)
	writer.size += int64(n)
	return n, err
}
