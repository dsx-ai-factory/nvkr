// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && !cgo

package nvkedemo

import (
	"errors"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDarwinRetainedNoCGOCapabilityRefusal(t *testing.T) {
	var coded *nvkrerr.Error
	if err := checkRetainedPeerCapability(t.Context()); !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
		t.Fatal("unsupported build invented process ownership", err)
	}
}
