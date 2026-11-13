// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type cancelDiskInspectionCapture struct{ cancel context.CancelFunc }

func (reader cancelDiskInspectionCapture) Read(raw []byte) (int, error) {
	reader.cancel()
	raw[0] = 'x'
	return 1, nil
}
func TestDiskInspectionCaptureDistinguishesCancellationAndIntegrity(t *testing.T) {
	digest := sha256.Sum256([]byte("x"))
	pin := "sha256:" + hex.EncodeToString(digest[:])
	for _, tt := range []struct {
		name   string
		input  string
		digest string
		size   int64
		cancel bool
		want   e.Code
	}{
		{name: "exact", input: "x", digest: pin, size: 1},
		{name: "digest mismatch", input: "y", digest: pin, size: 1, want: e.CodeVerifyFailed},
		{name: "size mismatch", input: "x", digest: pin, size: 2, want: e.CodeVerifyFailed},
		{name: "cancel during capture", digest: pin, size: 1, cancel: true, want: e.CodeTimeout},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var input io.Reader = strings.NewReader(tt.input)
			if tt.cancel {
				input = cancelDiskInspectionCapture{cancel: cancel}
			}
			subject, err := holdDiskInspectionSubject(ctx, input, tt.digest, tt.size)
			if sbomCode(err) != tt.want || (subject != nil) != (tt.want == "") {
				t.Fatalf("subject=%v error=%v, want %s", subject, err, tt.want)
			}
			if subject != nil {
				_ = subject.Close()
			}
		})
	}
}
