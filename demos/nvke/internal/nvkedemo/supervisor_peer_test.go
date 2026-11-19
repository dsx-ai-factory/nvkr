// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSupervisorPeerAdmissionCannotBeReconstructedFromDescriptions(t *testing.T) {
	bounded, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, test := range []struct {
		name string
		peer *SupervisorPeer
		ctx  context.Context
	}{
		{"nil peer", nil, bounded},
		{"unissued peer", &SupervisorPeer{}, bounded},
		{"nil context", &SupervisorPeer{}, nil},
		{"unbounded context", &SupervisorPeer{}, t.Context()},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.peer.WaitExit(test.ctx)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage {
				t.Fatalf("description produced an issued observation: %v", err)
			}
			if err := test.peer.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if runtime.GOOS != "linux" {
		err := CheckSupervisorPeerCapability(t.Context())
		var coded *nvkrerr.Error
		if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
			t.Fatalf("unsupported platform silently fell back: %v", err)
		}
	}
}
