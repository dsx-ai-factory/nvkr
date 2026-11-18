// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCatalogAliasesSelectTheSameCatalog(t *testing.T) {
	for _, command := range []string{"list", "validate", "digest"} {
		var expected string
		for _, flag := range []string{"--catalog", "--data", "--dir"} {
			t.Run(command+flag, func(t *testing.T) {
				output, err := runCatalogCommand(t, "catalog", command, flag, layeredCatalogPath(t))
				if err != nil {
					t.Fatal(err)
				}
				if expected == "" {
					expected = output
				} else if output != expected {
					t.Fatalf("alias changed output: %s", output)
				}
			})
		}
	}
}

func TestCatalogAliasConflictsAreCodedBeforeIO(t *testing.T) {
	for _, prefix := range [][]string{
		{"catalog", "list"}, {"catalog", "find"}, {"catalog", "index"}, {"catalog", "pin"},
		{"catalog", "bump"}, {"catalog", "pin-artifacts"}, {"definition", "init"}, {"resolve"},
	} {
		for _, aliases := range [][]string{{"--catalog", "--data"}, {"--catalog", "--dir"}, {"--data", "--dir"}} {
			t.Run(strings.Join(prefix, " ")+strings.Join(aliases, " "), func(t *testing.T) {
				args := append([]string{"nvkr"}, prefix...)
				args = append(args, aliases[0], "must-not-read-a", aliases[1], "must-not-read-b")
				_, err := runCLIWithCapturedStdout(t, newApp(), args...)
				assertLifecycleCommandCode(t, err, e.CodeUsage)
				if !strings.Contains(err.Error(), "same flag") {
					t.Fatalf("did not reject alias conflict: %v", err)
				}
			})
		}
	}
}
