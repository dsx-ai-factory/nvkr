package pipeline

import (
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// rejectUnsupportedExecutionRequirements is the admission boundary until an
// adapter can authenticate and enforce the compiler-selected Environment class on
// the realized runtime. Structural validation and identity inspection remain
// available, but no adapter may silently ignore a request.
func rejectUnsupportedExecutionRequirements(nodes []ir.Node, adapter string) error {
	for _, node := range nodes {
		if node.Requirements != nil {
			return e.New(e.CodeEngineCapability,
				"%s cannot enforce execution requirements for node %q", adapter, node.ID)
		}
	}
	return nil
}
