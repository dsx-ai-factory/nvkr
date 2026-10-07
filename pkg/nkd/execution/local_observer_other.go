// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package execution

import (
	"context"
	"os"
	"runtime"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type systemLocalObserver struct{}

func (systemLocalObserver) Open(context.Context, *os.File) (localObservationLease, error) {
	return nil, e.New(e.CodeEngineCapability,
		"local host observation requires Linux; host is %s/%s", runtime.GOOS, runtime.GOARCH)
}
