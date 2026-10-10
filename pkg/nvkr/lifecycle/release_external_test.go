// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestConsumerOptionsExposeNoCommandRunnerOrVerifierInjection(t *testing.T) {
	typeOfOptions := reflect.TypeOf(lifecycle.ConsumerOptions{})
	for _, forbidden := range []string{"Cosign", "Runner", "Verifier"} {
		if _, ok := typeOfOptions.FieldByName(forbidden); ok {
			t.Fatalf("public ConsumerOptions exposes authority bypass field %q", forbidden)
		}
	}
}
