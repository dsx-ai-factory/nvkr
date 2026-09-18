package aicr

import (
	"errors"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestAICRTupleValidationRequiresConcreteSupportedValues(t *testing.T) {
	for index, name := range []string{"service", "accelerator", "os", "intent", "platform"} {
		for _, value := range []string{"", "unknown"} {
			t.Run(name+"="+value, func(t *testing.T) {
				tuple := []string{"eks", "h100", "ubuntu", "training", "kubeflow"}
				tuple[index] = value
				err := ValidateValues(tuple[0], tuple[1], tuple[2], tuple[3], tuple[4])
				if index == 4 && value == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				want := e.CodeUnsupported
				if value == "" {
					want = e.CodeInvalidCatalog
				}
				assertValuesCode(t, err, want)
			})
		}
	}
	if err := ValidateValues("eks", "h100", "ubuntu", "training", "kubeflow"); err != nil {
		t.Fatal(err)
	}
}

func assertValuesCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}
