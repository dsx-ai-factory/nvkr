// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/testutil"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCompleteDocumentBytePolicy(t *testing.T) {
	for _, test := range []struct {
		name string
		size int
		code e.Code
	}{
		{name: "ordinary", size: 64},
		{name: "larger than a signing record", size: 2 << 20},
		{name: "complete inventory refused without truncation", size: MaxDocumentBytes + 1, code: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.size > 64 {
				testutil.RequireExtended(t)
			}
			bom, err := NewSubject("sha256:"+strings.Repeat("a", 64), "component", "sha256:"+strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			bom.Metadata.Props = append(bom.Metadata.Props, Property{Name: "nkd:zpayload", Value: strings.Repeat("x", test.size)})
			raw, err := bom.Marshal()
			if test.code == "" {
				if err != nil || len(raw) < test.size || len(raw) > MaxDocumentBytes {
					t.Fatalf("bytes %d, error %v", len(raw), err)
				}
				return
			}
			var coded *e.Error
			if raw != nil || !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("bytes %d, error %v; want no output and %s", len(raw), err, test.code)
			}
		})
	}
}
