// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pipeline

import (
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPackerKVMUnsupportedPlatform(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		want    e.Code
	}{
		{name: "disabled"},
		{name: "enabled", enabled: true, want: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection, err := observePackerKVMProjection(t.Context(), nil, test.enabled)
			if projection != nil || code(err) != test.want {
				t.Fatalf("projection=%v, error=%v, want nil/%s", projection, err, test.want)
			}
		})
	}
	if err := validateEnabledPackerContainerKVM(nil, nil); code(err) != e.CodeEngineCapability {
		t.Fatalf("KVM validation error=%v, want %s", err, e.CodeEngineCapability)
	}
}
