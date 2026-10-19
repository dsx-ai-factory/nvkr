// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package sign

import (
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func openPublicationRegular(path string) (*os.File, os.FileInfo, error) {
	return nil, nil, e.New(e.CodeEngineCapability,
		"crash-recoverable signed-document publication cannot safely open %s on this platform", path)
}
