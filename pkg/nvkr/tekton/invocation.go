// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tekton

import (
	"net/url"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ValidateInvocation admits only a canonical exact PipelineRun UID locator.
// Parsing grants no execution or provider authority. Tekton retries cannot
// inherit another Run's evidence merely because the human name is unchanged.
func ValidateInvocation(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" || u.ForceQuery || u.String() != raw {
		return e.New(e.CodeVerifyFailed, "Tekton invocation requires a canonical exact HTTPS Run URI")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	uid := u.Query().Get("uid")
	if len(parts) != 7 || parts[0] != "apis" || parts[1] != "tekton.dev" || parts[2] != "v1" || parts[3] != "namespaces" || !resourceName.MatchString(parts[4]) || parts[5] != "pipelineruns" || !resourceName.MatchString(parts[6]) || !resourceUID.MatchString(uid) || u.RawQuery != "uid="+uid {
		return e.New(e.CodeVerifyFailed, "Tekton invocation requires exact namespace, Run and UID")
	}
	return nil
}
