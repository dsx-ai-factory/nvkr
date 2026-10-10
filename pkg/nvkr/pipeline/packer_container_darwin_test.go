// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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

func TestAdmitVMImagePackerRejectsNonLinuxBeforeRuntimeResolution(t *testing.T) {
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	executor, err := session.AdmitVMImagePacker(t.Context(), nil, nil, "definitely-missing-runtime")
	if executor != nil || code(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "requires Linux") {
		t.Fatalf("non-Linux admission executor/error = %#v/%v, want nil/%s Linux rejection",
			executor, err, e.CodeEngineCapability)
	}
}
