// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"os"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestObservePackerKVMProjectionRejections(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "not-a-device")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	for _, test := range []struct {
		name     string
		device   *os.File
		enabled  bool
		canceled bool
		want     e.Code
	}{
		{name: "nil device", enabled: true, want: e.CodeEngineCapability},
		{name: "regular file", device: regular, enabled: true, want: e.CodeEngineCapability},
		{name: "canceled", enabled: true, canceled: true, want: e.CodeTimeout},
		{name: "disabled"},
		{name: "disabled with canceled context", canceled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			projection, err := observePackerKVMProjection(ctx, test.device, test.enabled)
			if projection != nil || code(err) != test.want {
				t.Fatalf("projection=%v, error=%v, want nil/%s", projection, err, test.want)
			}
		})
	}
}
