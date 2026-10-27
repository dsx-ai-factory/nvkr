// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package execution

import (
	"context"
	"os"
	"runtime"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type systemLocalObserver struct{}

func (systemLocalObserver) Open(context.Context, *os.File) (localObservationLease, error) {
	return nil, e.New(e.CodeEngineCapability,
		"local host observation requires Linux; host is %s/%s", runtime.GOOS, runtime.GOARCH)
}
