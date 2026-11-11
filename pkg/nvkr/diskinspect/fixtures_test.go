// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func rawDigest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func sbomCode(err error) e.Code {
	if coded, ok := err.(*e.Error); ok {
		return coded.Code
	}
	return ""
}
func helmTestArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for name, raw := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(raw))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
