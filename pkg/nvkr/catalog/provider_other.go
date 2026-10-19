// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package catalog

import (
	"context"
	"fmt"
)

func readCatalogRegularBeneathContext(context.Context, string, string) ([]byte, error) {
	return nil, fmt.Errorf("bounded no-follow catalog directories are unsupported on this platform")
}

func readCatalogDirectoryBeneathContext(context.Context, string, string) ([]string, error) {
	return nil, fmt.Errorf("bounded no-follow catalog directories are unsupported on this platform")
}
