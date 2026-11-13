// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// This separate command consumes only the exported diagnostic adapter API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func main() {
	engine := flag.String("engine", "github", "admitted engine")
	release := flag.String("release", "", "adapter release")
	flag.Parse()
	if err := factory.ServeProjectionAdapterForEngine(context.Background(), *engine, *release, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
