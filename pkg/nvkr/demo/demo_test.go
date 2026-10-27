// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package demo_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

func TestExternalModuleAndReleaseDependencyBoundary(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile(filepath.Join(repo, ".go-version"))
	if err != nil {
		t.Fatal(err)
	}
	consumer := t.TempDir()
	module := "module example.test/demo-consumer\n\ngo " + strings.TrimSpace(string(version)) + "\n\nrequire github.com/dsx-ai-factory/nvkr v0.0.0\nreplace github.com/dsx-ai-factory/nvkr => " + strconv.Quote(repo) + "\n"
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "demo_test.go"), []byte(consumerTest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name, directory string
		arguments       []string
	}{
		{"external consumer", consumer, []string{"test", "-mod=mod", "-count=1", "."}},
		{"release dependencies", repo, []string{"list", "-deps", "./cmd/nvkr"}},
	} {
		t.Run(check.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, "go", check.arguments...)
			command.Dir = check.directory
			command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=")
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			err := ownedcmd.Run(ctx, command)
			if err != nil {
				t.Fatalf("%s: %v\n%s", check.name, err, output.String())
			}
			if check.name == "release dependencies" {
				for _, dependency := range strings.Fields(output.String()) {
					for _, excluded := range []string{"github.com/dsx-ai-factory/nvkr/internal/nvkedemo", "github.com/dsx-ai-factory/nvkr/pkg/nvkr/demo", "github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity", "github.com/spiffe/", "google.golang.org/grpc"} {
						if strings.HasPrefix(dependency, excluded) {
							t.Fatalf("release CLI imports diagnostic runtime dependency %s", dependency)
						}
					}
				}
			}
		})
	}
}

const consumerTest = `package consumer

import (
	"context"
	"errors"
	"testing"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/demo"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNoWorkAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context()); cancel()
	called := false
	use := func(context.Context, *demo.Site) error { called = true; return nil }
	for _, tc := range []struct { name string; call func() error; code e.Code }{
		{"nil context", func() error { return demo.WithSite(nil, demo.Config{}, use) }, e.CodeUsage},
		{"nil callback", func() error { return demo.WithSite(t.Context(), demo.Config{}, nil) }, e.CodeUsage},
		{"canceled", func() error { return demo.WithSite(canceled, demo.Config{}, use) }, e.CodeTimeout},
		{"zero site", func() error { _, err := new(demo.Site).Kubeconfig(t.Context()); return err }, e.CodeUsage},
		{"zero tenant", func() error { return new(demo.Tenant).Delete(t.Context()) }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("unexpected admission: %v", err)
			}
		})
	}
	if called { t.Fatal("invalid admission reached the deployment callback") }
}
`
