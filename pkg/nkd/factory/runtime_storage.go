package factory

import e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"

// Complete-Plan adapters do not yet project a storage override to every
// producer, utility and runner runtime. A frozen override must not be ignored.
// The separate held inspector and direct-Packer owners support typed selection.
func rejectUnsupportedRuntimeStorage(plan *Plan, adapter string) error {
	if driver := plan.Environment.Runtime.PodmanStorageDriver; driver != "" {
		return e.New(e.CodeEngineCapability,
			"%s cannot enforce Environment.runtime.podmanStorageDriver %q across the complete Plan", adapter, driver)
	}
	return nil
}
