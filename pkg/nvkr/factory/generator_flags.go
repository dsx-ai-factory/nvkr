// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import "runtime/debug"

// UnstampedGeneratorVersion and UnstampedGeneratorCommit are the generator
// label parts of a build without -X main.version and -X main.commit stamps.
const (
	UnstampedGeneratorVersion = "dev"
	UnstampedGeneratorCommit  = "unknown"
)

// GeneratorVersion returns the generator label that Plans record and that
// generated bootstraps stamp into the runner. Linker stamps win. A `go install
// …/cmd/nvkr@<version>` build cannot carry stamps, so an unstamped module-mode
// build is labeled from its build information, which records no VCS revision:
// an exact release keeps an unknown commit, and a pseudo-version (an install of
// a commit) is a development build labeled with the revision it encodes. Every
// other unstamped build, including a checkout build, keeps the development
// label, so it regenerates committed bundles independently of the checked-out commit.
func GeneratorVersion(version, commit string) string {
	if version == UnstampedGeneratorVersion && commit == UnstampedGeneratorCommit {
		info, ok := readGitHubGeneratorBuildInfo()
		switch {
		case releasedGeneratorBuild(info, ok):
			version = info.Main.Version
		case pseudoVersionGeneratorBuild(info, ok):
			commit = info.Main.Version[len(info.Main.Version)-12:]
		}
	}
	return version + " (" + commit + ")"
}

// pseudoVersionGeneratorBuild reports whether info is a module-mode install of
// an NVKR commit. Checkout builds also record pseudo-versions, but no checksum.
func pseudoVersionGeneratorBuild(info *debug.BuildInfo, ok bool) bool {
	return moduleGeneratorBuild(info, ok) && githubPseudoVersionPattern.MatchString(info.Main.Version)
}

// GeneratorLinkerFlags returns the validated Go linker flags that reproduce a
// Plan's generator version label. Adapters use the same parser as Run rendering;
// this descriptive label grants no build or release authority.
func GeneratorLinkerFlags(version string) (string, error) {
	return generatorLinkerFlags(version)
}
