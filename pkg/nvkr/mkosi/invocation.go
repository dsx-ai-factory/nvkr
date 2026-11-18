// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mkosi

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
)

// Invocation describes one fixed, offline mkosi operation. All locations are
// inside the admitted namespace. The execution owner creates a fresh Project
// containing only the exact recipe and PackageConfiguration, and authenticates
// every package/file before launch. This description grants no execution rights.
type Invocation struct {
	Root                 string
	Format               string
	Project              string
	Workspace            string
	Cache                string
	PackageCache         string
	Packages             string
	OutputDirectory      string
	Output               string
	Manifest             string
	SourceDateEpoch      int64
	PackageConfiguration string
	Command              []string
}

// Invocations first assemble all package roots as directories. The final pass
// packages the last root, file materials and retained original package reports
// into an uncompressed tar for the execution owner to wrap as one OCI layer.
// It does not resolve or install more packages.
func (spec *Spec) Invocations(platform string, bindings []string) ([]Invocation, error) {
	if err := spec.Validate(platform, bindings); err != nil {
		return nil, err
	}
	architecture := map[string]string{"linux/amd64": "x86-64", "linux/arm64": "arm64"}[platform]
	invocations := make([]Invocation, 0, len(spec.Roots)+1)
	for _, root := range spec.Roots {
		operation, err := rootInvocation(root, architecture, "directory", "/work/roots/"+root.Name)
		if err != nil {
			return nil, err
		}
		var packages strings.Builder
		packages.WriteString("[Content]\nPackages=\n")
		for _, item := range root.Inputs.Packages {
			fmt.Fprintf(&packages, "    %s=%s\n", item.Name, item.Version)
		}
		operation.PackageConfiguration = packages.String()
		operation.Command = append(operation.Command, "--package-directory="+operation.Packages)
		for _, mount := range root.Mounts {
			operation.Command = append(operation.Command, "--extra-tree="+rootOutput(mount.Root)+":"+mount.Path)
		}
		operation.Command = append(operation.Command, "build")
		invocations = append(invocations, operation)
	}
	last := spec.Roots[len(spec.Roots)-1]
	operation, err := rootInvocation(last, architecture, "tar", "/work/packaging")
	if err != nil {
		return nil, err
	}
	operation.Command = append(operation.Command, "--base-tree="+rootOutput(last.Name), "--extra-tree=/inputs/evidence")
	if len(spec.Files)+len(spec.Copies)+len(spec.Contents) != 0 {
		operation.Command = append(operation.Command, "--extra-tree=/inputs/file-materials")
	}
	operation.Command = append(operation.Command, "build")
	invocations = append(invocations, operation)
	return invocations, nil
}

func rootInvocation(root Root, architecture, format, state string) (Invocation, error) {
	snapshot, err := time.Parse("20060102T150405Z", root.Inputs.Snapshot)
	if err != nil {
		return Invocation{}, e.Wrap(e.CodeInvalidCatalog, err, "mkosi root requires an exact snapshot timestamp")
	}
	inputState := strings.Replace(state, "/work/", "/inputs/", 1)
	operation := Invocation{Root: root.Name, Format: format, Project: path.Join(inputState, "project"),
		Workspace: path.Join(state, "workspace"), Cache: path.Join(state, "cache"), PackageCache: path.Join(state, "package-cache"),
		Packages: path.Join(inputState, "packages"), OutputDirectory: path.Join(state, "output"), SourceDateEpoch: snapshot.Unix()}
	operation.Output = path.Join(operation.OutputDirectory, root.Name)
	operation.Manifest = operation.Output + ".manifest"
	extension := ""
	if format == "tar" {
		extension = "tar"
		operation.Output += ".tar"
		// BaseTrees packaging reports only newly installed packages. This
		// pass installs none and carries the original checked root reports.
		operation.Manifest = ""
	}
	// Python reads only the authenticated source checkout. Isolated mode and
	// disabled site initialization exclude user modules and ambient startup hooks.
	operation.Command = []string{"/usr/bin/python3", "-I", "-S", "-c",
		"import runpy,sys; sys.path.insert(0, '/src'); runpy.run_module('mkosi', run_name='__main__')",
		"--directory=" + operation.Project,
		"--distribution=" + root.Distribution, "--release=" + root.Release, "--architecture=" + architecture,
		"--format=" + format, "--output=" + root.Name, "--output-extension=" + extension, "--image-id=" + root.Name,
		"--output-directory=" + operation.OutputDirectory, "--workspace-directory=" + operation.Workspace,
		"--cache-directory=" + operation.Cache, "--package-cache-directory=" + operation.PackageCache,
		"--local-mirror=file://" + operation.Packages, "--cache-only=always", "--incremental=no", "--cache-key=nvkr-vm-runtime",
		fmt.Sprintf("--source-date-epoch=%d", operation.SourceDateEpoch), "--extra-search-path=", "--extra-search-path=" + ArchiveToolsDirectory,
		"--bootable=no", "--compress-output=no", "--split-artifacts=", "--manifest-format=json",
		"--build-sources=", "--with-network=no", "--with-docs=yes", "--with-recommends=no", "--clean-package-metadata=no",
		"--remove-files=/var/cache/*,/var/log/*", "--history=no"}
	return operation, nil
}

func rootOutput(name string) string { return "/work/roots/" + name + "/output/" + name }

// CheckReport binds the package report to this root and operation in addition
// to checking equality of every installed package against the retained closure.
func (operation Invocation) CheckReport(root Root, raw []byte) error {
	if err := root.Inputs.CheckMkosiClosure(raw); err != nil {
		return err
	}
	var manifest material.MkosiManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding checked mkosi package report")
	}
	if operation.Root != root.Name || manifest.Config.Name != root.Name || manifest.Config.Distribution != root.Distribution ||
		manifest.Config.Release != root.Release || manifest.Config.OutputFormat != operation.Format || manifest.Config.Version != "" {
		return e.New(e.CodeVerifyFailed, "mkosi package report changed its selected root or assembly operation")
	}
	return nil
}

// PackageFilename is the APT adapter's local repository filename. Mkosi derives
// essential package names from the portion before '_'; a digest-only filename
// would break its bootstrap. The caller must validate the complete inputs first.
func PackageFilename(item material.PackageInput) string {
	return item.Name + "_" + item.Version + "_" + item.Architecture + ".deb"
}
