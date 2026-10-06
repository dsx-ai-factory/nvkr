package pipeline

import (
	"bytes"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// VMImagePackerNetworkAdmission is a process-local receipt proving that one
// validated Environment authorized the exact networked Packer selections held
// by nodes. Its fields remain private so a caller-created IR node is only a
// description, never network authority.
type VMImagePackerNetworkAdmission struct {
	environmentDigest string
	nodes             map[string][]byte
	storageDriver     definition.PodmanStorageDriver
}

// AuthorizeVMImagePackerNetwork validates the authored Environment and binds
// its producer-declared network policy to the exact canonical bytes of every
// networked selection. Offline selections need no network receipt.
func AuthorizeVMImagePackerNetwork(
	environment definition.Environment,
	expectedEnvironmentDigest string,
	selections []VMImagePackerSelection,
) (*VMImagePackerNetworkAdmission, error) {
	environmentDigest, err := environment.Digest()
	if err != nil {
		return nil, err
	}
	if !canonical.IsDigest(expectedEnvironmentDigest) {
		return nil, e.New(e.CodeUnverifiedRef,
			"Packer network admission requires an exact Plan Environment identity")
	}
	if environmentDigest != expectedEnvironmentDigest {
		return nil, e.New(e.CodeUnverifiedRef,
			"Packer network admission Environment differs from the Plan Environment identity")
	}
	policy := environment.Runtime.BuildNetworkPolicy
	if policy == "" {
		policy = definition.BuildNetworkOfflineOnly
	}
	receipt := &VMImagePackerNetworkAdmission{
		environmentDigest: environmentDigest,
		storageDriver:     environment.Runtime.PodmanStorageDriver,
		nodes:             make(map[string][]byte),
	}
	for _, selection := range selections {
		if err := selection.Node.Validate(); err != nil {
			return nil, err
		}
		if selection.Node.Network != ir.NetworkDefault {
			continue
		}
		if policy != definition.BuildNetworkProducerDeclared {
			return nil, e.New(e.CodeEngineCapability,
				"Packer node %q requests network access but Environment policy is %q",
				selection.Node.ID, policy)
		}
		raw, err := canonical.Marshal(selection.Node)
		if err != nil {
			return nil, err
		}
		if _, duplicate := receipt.nodes[selection.Subject]; duplicate {
			return nil, e.New(e.CodeInvalidCatalog,
				"Packer network admission repeats Plan subject %q", selection.Subject)
		}
		receipt.nodes[selection.Subject] = raw
	}
	return receipt, nil
}

func requireVMImagePackerNetworkAdmission(
	receipt *VMImagePackerNetworkAdmission,
	selection VMImagePackerSelection,
) error {
	if selection.Node.Network != ir.NetworkDefault {
		return nil
	}
	if receipt == nil || receipt.environmentDigest == "" {
		return e.New(e.CodeEngineCapability,
			"Packer node %q has no Environment network admission", selection.Node.ID)
	}
	want, ok := receipt.nodes[selection.Subject]
	if !ok {
		return e.New(e.CodeEngineCapability,
			"Packer node %q is not selected by its Environment network admission", selection.Node.ID)
	}
	got, err := canonical.Marshal(selection.Node)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return e.New(e.CodePipelineDrift,
			"Packer node %q changed after Environment network admission", selection.Node.ID)
	}
	return nil
}
