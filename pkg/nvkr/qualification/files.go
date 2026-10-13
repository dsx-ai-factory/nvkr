// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// FilesOptions retains source compatibility with the retired file publisher.
// Artifact checking remains available through lifecycle.CheckQualificationFiles.
type FilesOptions struct {
	ArtifactFiles         lifecycle.QualificationFilesOptions
	QualificationWorkflow string
	Signer                Options
	Policy                Policy
}

// PublishFiles refuses before provider, file, registry or signer work because
// caller-selected files cannot establish authenticated hosted worker completion.
// Deprecated: use PublishTriggeredRun with independent qualification policy.
func PublishFiles(ctx context.Context, _ FilesOptions) (*Publication, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "qualification requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualification canceled")
	}
	return nil, e.New(e.CodeEngineCapability, "file-based qualification cannot authenticate hosted smoke completion; use PublishTriggeredRun with independent policy")
}
