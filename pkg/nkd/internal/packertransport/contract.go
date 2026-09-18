// Package packertransport owns the bounded argv contract shared by Packer
// catalog authoring and execution. It validates only NKD-owned transport
// framing. The pinned Packer executable remains authoritative for HCL variable
// declarations and value semantics.
package packertransport

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
)

const (
	// MaxPublicVariables bounds authored public variables. The three
	// NKD-owned architecture, material-manifest, and output-directory bindings
	// are reserved bindings and are not included in this count.
	MaxPublicVariables = 256
	// MaxPublicVariableArgBytes bounds the aggregate bytes in public
	// name=value arguments. It deliberately excludes reserved bindings whose
	// runtime values include private execution paths.
	MaxPublicVariableArgBytes = 1 << 20
	MaxVariableNameBytes      = 128
	MaxVariableValueBytes     = 64 << 10
)

// Variable is one public literal Packer variable transported as name=value.
type Variable struct {
	Name  string
	Value string
}

// ValidateVariables validates all reserved and public variable bindings as one
// argv namespace. It does not interpret HCL identifiers or variable types.
func ValidateVariables(reserved []string, public []Variable) error {
	if len(public) > MaxPublicVariables {
		return fmt.Errorf("packer build has %d public variables, exceeding the %d-variable bound",
			len(public), MaxPublicVariables)
	}

	seen := make(map[string]bool)
	for _, name := range reserved {
		if !ValidVariableName(name) {
			return fmt.Errorf("packer reserved variable name %q is not transport-safe", name)
		}
		if seen[name] {
			return fmt.Errorf("packer reserved variable name %q is duplicated", name)
		}
		seen[name] = true
	}

	argumentBytes := 0
	for _, variable := range public {
		if !ValidVariableName(variable.Name) {
			return fmt.Errorf("packer public variable name %q is not transport-safe", variable.Name)
		}
		if !ValidVariableValue(variable.Value) {
			return fmt.Errorf("packer public variable %q has a value that is not transport-safe", variable.Name)
		}
		if seen[variable.Name] {
			return fmt.Errorf("packer public variable %q duplicates another binding", variable.Name)
		}
		argumentBytes += len(variable.Name) + 1 + len(variable.Value)
		if argumentBytes > MaxPublicVariableArgBytes {
			return fmt.Errorf("packer public variable bindings exceed the %d-byte argv bound",
				MaxPublicVariableArgBytes)
		}
		seen[variable.Name] = true
	}
	return nil
}

// ValidVariableName reports whether value can be transported unambiguously in
// Packer's -var name=value framing. '=' is rejected because accepting it would
// change which name and value the subprocess receives.
func ValidVariableName(value string) bool {
	return validTransport(value, MaxVariableNameBytes, false) && !strings.ContainsRune(value, '=')
}

// ValidVariableValue reports whether a public literal value is bounded and
// control-free. Empty values are allowed and retain their exact meaning.
func ValidVariableValue(value string) bool {
	return validTransport(value, MaxVariableValueBytes, true)
}

func validTransport(value string, maxBytes int, allowEmpty bool) bool {
	return safetext.Valid(value, maxBytes, allowEmpty)
}
