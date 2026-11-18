// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mkosi

import "slices"

// LogicalSpec excludes acquisition locations while retaining every selected
// content pin. Only the owning IR codec computes its Build identity, after
// validating the complete transport-bearing graph.
type LogicalSpec struct {
	ConfigurationDigest string            `json:"configurationDigest"`
	Roots               []LogicalRoot     `json:"roots"`
	Files               []File            `json:"files,omitempty"`
	Copies              []PackageFile     `json:"copies,omitempty"`
	Contents            []FileMeasurement `json:"contents,omitempty"`
	Measurements        []FileMeasurement `json:"measurements,omitempty"`
}

type LogicalRoot struct {
	Name         string        `json:"name"`
	Distribution string        `json:"distribution"`
	Release      string        `json:"release"`
	Inputs       LogicalInputs `json:"inputs"`
	Mounts       []Mount       `json:"mounts,omitempty"`
}

type LogicalInputs struct {
	SchemaVersion  string          `json:"schemaVersion"`
	PackageManager string          `json:"packageManager"`
	Architecture   string          `json:"architecture"`
	Snapshot       string          `json:"snapshot"`
	Repositories   []RepositoryPin `json:"repositories"`
	Packages       []PackagePin    `json:"packages"`
}

type RepositoryPin struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type PackagePin struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	Architecture     string `json:"architecture"`
	Digest           string `json:"digest"`
	Size             int64  `json:"size"`
	RepositoryDigest string `json:"repositoryDigest"`
}

// Logical returns a defensive description, not a receipt or identity digest.
func (spec *Spec) Logical() *LogicalSpec {
	if spec == nil {
		return nil
	}
	result := &LogicalSpec{ConfigurationDigest: spec.ConfigurationDigest,
		Files: slices.Clone(spec.Files), Copies: slices.Clone(spec.Copies), Measurements: slices.Clone(spec.Measurements)}
	for _, content := range spec.Contents {
		result.Contents = append(result.Contents, FileMeasurement{Path: content.Path, Digest: content.Digest})
	}
	for _, root := range spec.Roots {
		logical := LogicalRoot{Name: root.Name, Distribution: root.Distribution, Release: root.Release, Mounts: slices.Clone(root.Mounts),
			Inputs: LogicalInputs{SchemaVersion: root.Inputs.SchemaVersion, PackageManager: root.Inputs.PackageManager,
				Architecture: root.Inputs.Architecture, Snapshot: root.Inputs.Snapshot}}
		for _, repository := range root.Inputs.Repositories {
			logical.Inputs.Repositories = append(logical.Inputs.Repositories, RepositoryPin{repository.Digest, repository.Size})
		}
		slices.SortFunc(logical.Inputs.Repositories, func(a, b RepositoryPin) int {
			if a.Digest < b.Digest {
				return -1
			}
			if a.Digest > b.Digest {
				return 1
			}
			return 0
		})
		for _, item := range root.Inputs.Packages {
			logical.Inputs.Packages = append(logical.Inputs.Packages, PackagePin{
				Name: item.Name, Version: item.Version, Architecture: item.Architecture, Digest: item.Digest,
				Size: item.Size, RepositoryDigest: item.RepositoryDigest})
		}
		result.Roots = append(result.Roots, logical)
	}
	return result
}
