// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/runtimeexec"
)

// RuntimeNamespaceEntry dispatches only the sealed namespace child's private
// protocol. The command must exit after any handled return: the child can have
// partially applied irreversible restrictions. This entry grants no production,
// runtime-use, Run completion, or publication authority.
func RuntimeNamespaceEntry(arguments []string) (handled bool, err error) {
	if len(arguments) == 0 || arguments[0] != runtimeexec.EntryArgument {
		return false, nil
	}
	if len(arguments) != 1 {
		return true, e.New(e.CodeUsage, "runtime namespace bootstrap accepts only its inherited sealed request")
	}
	return true, runtimeexec.Entry()
}
