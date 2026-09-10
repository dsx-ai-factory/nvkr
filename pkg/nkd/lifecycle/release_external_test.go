package lifecycle_test

import (
	"reflect"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

func TestConsumerOptionsExposeNoCommandRunnerOrVerifierInjection(t *testing.T) {
	typeOfOptions := reflect.TypeOf(lifecycle.ConsumerOptions{})
	for _, forbidden := range []string{"Cosign", "Runner", "Verifier"} {
		if _, ok := typeOfOptions.FieldByName(forbidden); ok {
			t.Fatalf("public ConsumerOptions exposes authority bypass field %q", forbidden)
		}
	}
}
