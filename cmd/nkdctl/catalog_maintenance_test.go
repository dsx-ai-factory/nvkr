package main

import (
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCatalogMaintenanceRejectsBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"refresh timeout", []string{"refresh", "--config", "absent", "--timeout", "0s"}, e.CodeUsage},
		{"refresh positional", []string{"refresh", "--config", "absent", "extra"}, e.CodeUsage},
		{"refresh missing input", []string{"refresh", "--config", "absent"}, e.CodeInvalidCatalog},
		{"workflow engine", []string{"update-ci", "--config", "absent", "--engine", "gitlab"}, e.CodeEngineCapability},
		{"workflow timeout", []string{"update-ci", "--config", "absent", "--timeout", "-1s"}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := runCatalogCommand(t, append([]string{"catalog"}, tc.args...)...)
			if got := cliErrorCode(err); got != tc.code {
				t.Fatalf("code %s, want %s: %v", got, tc.code, err)
			}
			if output != "" {
				t.Fatalf("failure emitted output: %q", output)
			}
		})
	}
}
