// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

const (
	GitLabWorkflowPath = ".gitlab/nvkr-run/pipeline.yml"
	GitLabPlanPath     = ".gitlab/nvkr-run/plan.json"
	GitLabManifestPath = ".gitlab/nvkr-run/manifest.json"
)

// GitLab owns a finite include directory. The repository's root .gitlab-ci.yml
// remains operator owned and can never be retired by bundle regeneration.
var gitlabLayout = generationLayout{
	workflowPath: GitLabWorkflowPath,
	manifestPath: GitLabManifestPath,
	planPath:     GitLabPlanPath,
}

func gitlabWorkflowNamespace(entry string) (generationLayout, bool) {
	return gitlabLayout, entry == GitLabWorkflowPath
}

// gitlabLayoutFor binds the admitted engine to its fixed finite include
// directory; the operator-owned root pipeline is never a bundle member.
func gitlabLayoutFor(*Plan) generationLayout {
	return gitlabLayout
}
