package factory

import (
	"context"
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// VMRuntimeAdmission is an opaque process-local capability for one exact
// Plan-selected VM build. It carries no provider, reservation, qualification,
// or cross-process authority.
type VMRuntimeAdmission struct {
	inner *execution.LocalVMAdmission
}

// VMRuntime exposes only held descriptor capabilities during one admitted
// execution callback. It deliberately exposes no workspace or KVM path.
type VMRuntime struct {
	inner *execution.LocalVMRuntime
}

// AdmitLocalVMBuild validates the complete Plan, resolves one exact vm-image
// producer, and authenticates its selected execution class against the current
// Linux host and held workspace. It performs no source, subprocess, output, or
// workspace mutation.
func AdmitLocalVMBuild(
	ctx context.Context,
	plan *Plan,
	nodeID string,
	workspace *os.File,
) (*VMRuntimeAdmission, error) {
	request, err := localVMRequestForPlan(plan, nodeID)
	if err != nil {
		return nil, err
	}
	admission, err := execution.AdmitLocalVM(ctx, request, workspace)
	if err != nil {
		return nil, err
	}
	return &VMRuntimeAdmission{inner: admission}, nil
}

// Execute consumes an admission exactly once, revalidates the exact Plan node,
// and runs work under the authored executor deadline. The callback must use the
// supplied context for all blocking work and subprocess ownership.
func (admission *VMRuntimeAdmission) Execute(
	ctx context.Context,
	plan *Plan,
	nodeID string,
	work func(context.Context, *VMRuntime) error,
) error {
	if admission == nil || admission.inner == nil {
		return e.New(e.CodeEngineCapability, "VM runtime admission is absent")
	}
	if work == nil {
		return e.New(e.CodeUsage, "VM runtime execution requires work")
	}
	request, err := localVMRequestForPlan(plan, nodeID)
	if err != nil {
		return err
	}
	return admission.inner.Execute(ctx, request, func(runCtx context.Context, runtime *execution.LocalVMRuntime) error {
		return work(runCtx, &VMRuntime{inner: runtime})
	})
}

// HoldWorkspace duplicates the admitted workspace directory capability. The
// returned handle is automatically closed when Execute returns.
func (runtime *VMRuntime) HoldWorkspace() (*os.File, error) {
	if runtime == nil || runtime.inner == nil {
		return nil, e.New(e.CodeEngineCapability, "VM runtime is absent")
	}
	return runtime.inner.HoldWorkspace()
}

// HoldKVM duplicates the admitted KVM device when the Plan selected KVM. The
// returned handle is automatically closed when Execute returns.
func (runtime *VMRuntime) HoldKVM() (*os.File, error) {
	if runtime == nil || runtime.inner == nil {
		return nil, e.New(e.CodeEngineCapability, "VM runtime is absent")
	}
	return runtime.inner.HoldKVM()
}

// Close invalidates an unused admission and releases its held capabilities.
func (admission *VMRuntimeAdmission) Close() error {
	if admission == nil || admission.inner == nil {
		return nil
	}
	return admission.inner.Close()
}

func localVMRequestForPlan(plan *Plan, nodeID string) (execution.LocalVMRequest, error) {
	if plan == nil {
		return execution.LocalVMRequest{}, e.New(e.CodeUsage,
			"VM runtime admission requires a Plan")
	}
	if err := plan.Validate(); err != nil {
		return execution.LocalVMRequest{}, err
	}
	var selected Node
	for _, node := range plan.Graph.Nodes {
		if node.ID == nodeID {
			selected = node
			break
		}
	}
	if selected.ID == "" || selected.Kind != NodeBuild || selected.IRNode == "" {
		return execution.LocalVMRequest{}, e.New(e.CodeEngineCapability,
			"Plan node %q is not an executable VM build", nodeID)
	}
	_, instruction, err := plan.producerInstruction(selected)
	if err != nil {
		return execution.LocalVMRequest{}, err
	}
	if instruction.Kind != ir.KindBuild || instruction.BuilderType != ir.BuilderVMImage {
		return execution.LocalVMRequest{}, e.New(e.CodeEngineCapability,
			"Plan node %q is not a vm-image producer", nodeID)
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return execution.LocalVMRequest{}, err
	}
	currentDigest, err := plan.Digest()
	if err != nil {
		return execution.LocalVMRequest{}, err
	}
	return execution.LocalVMRequest{
		Subject:      identity + "/" + currentDigest + "/" + selected.ID,
		Requirements: execution.Clone(instruction.Requirements),
		Binding:      execution.CloneBinding(instruction.ExecutionBinding),
	}, nil
}
