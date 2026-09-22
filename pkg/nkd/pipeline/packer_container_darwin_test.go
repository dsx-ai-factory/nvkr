//go:build darwin

package pipeline

import (
	"context"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestRunPackerContainerRejectsNonLinuxHostBeforeRuntimeWork(t *testing.T) {
	runtime := &recordingPackerRuntime{endpoint: qualifiedRuntimeEndpoint}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	artifact, err := runPackerContainer(ctx, packerContainerRunRequest{runtime: runtime})
	if artifact != nil || code(err) != e.CodeEngineCapability {
		t.Fatalf("non-Linux artifact/error = %#v/%v, want nil/%s", artifact, err, e.CodeEngineCapability)
	}
	if len(runtime.calls) != 0 || len(runtime.cleanups) != 0 {
		t.Fatalf("non-Linux rejection caused runtime work: calls=%#v cleanups=%#v", runtime.calls, runtime.cleanups)
	}
}
