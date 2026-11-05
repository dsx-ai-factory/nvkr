// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGitHubPartitionName(t *testing.T) {
	for _, test := range []struct {
		name, label, want string
		count             int
	}{
		{"single output", "Build controller (controller/linux/arm64)", "p03 Build controller/linux/arm64", 1},
		{"multiple outputs", "Build and package producer evidence for nvke-node-image-arm64", "p03 Build nvke-node-image-arm64 +4", 5},
		{"acquisition", "Acquire pinned capi-controller-image-arm64/pull-capi-controller-image", "p03 Acquire capi-controller-image-arm64 +1", 2},
		{"prepared acquisition", "Acquire and prepare controller/linux/arm64", "p03 Acquire and prepare controller/linux/arm64", 1},
		{"fetch", "Fetch pinned source for controller (controller/linux/amd64)", "p03 Fetch controller/linux/amd64", 1},
		{"verification", "Verify artifacts and generate SBOMs: controller-arm64", "p03 Verify controller-arm64 +1", 2},
		{"release", "Verify release and deliverable signatures: output-controller", "p03 Verify release controller", 1},
		{"release level", "Verify release and deliverable signatures: release", "p03 Verify release and deliverable signatures: release", 1},
		{"unclosed build slot", "Build controller (controller/linux/arm64", "p03 Build controller (controller/linux/arm64", 1},
		{"package", "Package publication bundle: publication-controller", "p03 Package controller", 1},
		{"publication", "Publish output-controller", "p03 Publish controller", 1},
		{"runner", "Prepare NVKR runner (ubuntu-24.04)", "p03 Prepare NVKR runner (ubuntu-24.04) +3", 4},
		{"unnamed job", "", "p03 a +1", 2},
		{"long", strings.Repeat("x", 70) + " (linux/arm64)", "p03 " + strings.Repeat("x", 34) + "...xx (linux/arm64) +4", 5},
		{"unicode", strings.Repeat("界", 70) + " (linux/arm64)", "p03 " + strings.Repeat("界", 34) + "...界界 (linux/arm64) +4", 5},
		{"unicode largest suffix", strings.Repeat("界", 70) + " (linux/arm64)", "p03 " + strings.Repeat("界", 33) + "...界界 (linux/arm64) +63", 64},
	} {
		t.Run(test.name, func(t *testing.T) {
			jobs := githubYAMLMap("a", githubYAMLMap("name", githubYAMLString(test.label)))
			partition := githubExecutionPartition{Jobs: []string{"a"}}
			for i := 1; i < test.count; i++ {
				id := string(rune('a' + i))
				githubYAMLSet(jobs, id, githubYAMLMap("name", githubYAMLString("Build other-output (linux/amd64)")))
				partition.Jobs = append(partition.Jobs, id)
			}
			original := slices.Clone(partition.Jobs)
			got := githubPartitionName(3, partition, jobs)
			if got != test.want || utf8.RuneCountInString(got) > 60 || !utf8.ValidString(got) {
				t.Fatalf("name = %q, want %q (at most 60 characters)", got, test.want)
			}
			if !slices.Equal(partition.Jobs, original) {
				t.Fatal("naming changed partition membership order")
			}
			slices.Reverse(partition.Jobs)
			if again := githubPartitionName(3, partition, jobs); again != got {
				t.Fatalf("membership order changed name: %q versus %q", again, got)
			}
		})
	}
}

// Ties the shortener to the label producer, so a wording change there cannot
// silently fall back to the raw label.
func TestGitHubPartitionNameShortensProducerLabels(t *testing.T) {
	for _, test := range []struct {
		node Node
		want string
	}{
		{Node{ID: NodeAcquire + "/capi-controller-image-arm64/pull-capi-controller-image", Kind: NodeAcquire}, "Acquire capi-controller-image-arm64"},
		{Node{ID: "package/publication-controller", Kind: NodePackage, PackageKind: PackageOCI}, "Package controller"},
		{Node{ID: NodePublish + "/output-controller", Kind: NodePublish}, "Publish controller"},
		{Node{ID: NodeVerifyPublication + "/output-controller", Kind: NodeVerifyPublication}, "Verify release controller"},
		{Node{ID: NodeVerifyPublication + "/release", Kind: NodeVerifyPublication}, "Verify release and deliverable signatures: release"},
	} {
		if got := githubPartitionJobSummary(githubNodeLabel(&Plan{}, test.node)); got != test.want {
			t.Errorf("summary of %s = %q, want %q", test.node.ID, got, test.want)
		}
	}
}
