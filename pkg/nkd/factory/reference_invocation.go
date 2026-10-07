// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"net/url"
	"strconv"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// admitReferenceInvocation permits reuse only within one immutable GitHub run,
// from its current or an earlier attempt. It describes scope, not authenticity;
// the caller must still authenticate the exact predicate and publication bytes.
func admitReferenceInvocation(candidate, current string) error {
	parse := func(raw string) (string, uint64, bool) {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" ||
			u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.String() != raw {
			return "", 0, false
		}
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) != 7 || parts[0] == "" || parts[1] == "" || parts[2] != "actions" || parts[3] != "runs" || parts[5] != "attempts" {
			return "", 0, false
		}
		for _, value := range []string{parts[4], parts[6]} {
			n, err := strconv.ParseUint(value, 10, 64)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != value {
				return "", 0, false
			}
		}
		attempt, _ := strconv.ParseUint(parts[6], 10, 64)
		return strings.TrimSuffix(raw, "/attempts/"+parts[6]), attempt, true
	}
	left, previous, validLeft := parse(candidate)
	right, next, validRight := parse(current)
	if !validLeft || !validRight || left != right || previous > next {
		return e.New(e.CodeVerifyFailed, "publication invocation must be from the same GitHub run and a current or earlier attempt")
	}
	return nil
}
