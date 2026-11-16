// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"net/http"
	"strings"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func verifyTestDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func cliErrorCode(err error) nvkrerr.Code {
	var coded *nvkrerr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
