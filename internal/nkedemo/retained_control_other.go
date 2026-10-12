// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package nkedemo

import (
	"net"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func retainedRequesterUID(*net.UnixConn) uint32 { return ^uint32(0) }

func detachRetainedProcess() error {
	return nkderr.New(nkderr.CodeEngineCapability, "retained service ownership requires native Linux")
}
