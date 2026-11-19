// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/rootfs"
)

type runtimeTarInventory struct {
	LayerDigest string            `json:"layerDigest"`
	Entries     []runtimeTarEntry `json:"entries"`
}

// Entry order is retained: filesystem enumeration can change hard-link targets
// and tar bytes even when every extracted file has identical content.
type runtimeTarEntry struct {
	Path       string            `json:"path"`
	Type       byte              `json:"type"`
	Mode       int64             `json:"mode"`
	UID        int               `json:"uid"`
	GID        int               `json:"gid"`
	Uname      string            `json:"uname"`
	Gname      string            `json:"gname"`
	Size       int64             `json:"size"`
	Mtime      string            `json:"mtime"`
	Atime      string            `json:"atime"`
	Ctime      string            `json:"ctime"`
	Linkname   string            `json:"linkname"`
	PAX        map[string]string `json:"pax"`
	Content    string            `json:"sha256"`
	HeaderHash string            `json:"headerSHA256"`
}

// This test-only description is not an execution receipt. Stream the original
// layer while the Run owner is alive; do not duplicate its multi-GB payload.
func runtimeProofTarInventory(ctx context.Context, path, expected string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > rootfs.MaxLayerBytes {
		return nil, fmt.Errorf("runtime diagnostic requires a bounded regular layer")
	}
	return readRuntimeTarInventory(ctx, io.LimitReader(file, info.Size()+1), expected)
}

func readRuntimeTarInventory(ctx context.Context, source io.Reader, expected string) ([]byte, error) {
	layerHash, sectionHash := sha256.New(), sha256.New()
	reader := io.TeeReader(io.LimitReader(source, rootfs.MaxLayerBytes+1), io.MultiWriter(layerHash, sectionHash))
	archive := tar.NewReader(reader)
	inventory := runtimeTarInventory{LayerDigest: expected}
	metadataBytes := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Next consumes the preceding member's zero padding and all extended
		// headers as well as this member's main header. Keep that exact segment
		// hash to expose differences the decoded tar fields might hide.
		sectionHash.Reset()
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(inventory.Entries) >= 200000 || header.Size < 0 || header.Size > rootfs.MaxLayerBytes {
			return nil, fmt.Errorf("runtime diagnostic entry limit exceeded")
		}
		entry := runtimeTarEntry{Path: header.Name, Type: header.Typeflag, Mode: header.Mode,
			UID: header.Uid, GID: header.Gid, Uname: header.Uname, Gname: header.Gname, Size: header.Size,
			Mtime: header.ModTime.UTC().Format(time.RFC3339Nano), Atime: header.AccessTime.UTC().Format(time.RFC3339Nano),
			Ctime: header.ChangeTime.UTC().Format(time.RFC3339Nano), Linkname: header.Linkname, PAX: header.PAXRecords,
			HeaderHash: fmt.Sprintf("sha256:%x", sectionHash.Sum(nil))}
		content := sha256.New()
		if _, err := copyContext(ctx, content, archive); err != nil {
			return nil, err
		}
		entry.Content = fmt.Sprintf("sha256:%x", content.Sum(nil))
		raw, err := canonical.Marshal(entry)
		if err != nil {
			return nil, err
		}
		metadataBytes += len(raw)
		if metadataBytes > 64<<20 {
			return nil, fmt.Errorf("runtime diagnostic metadata limit exceeded")
		}
		inventory.Entries = append(inventory.Entries, entry)
	}
	// Include the archive's end padding in the exact layer hash.
	if _, err := copyContext(ctx, io.Discard, reader); err != nil {
		return nil, err
	}
	if got := fmt.Sprintf("sha256:%x", layerHash.Sum(nil)); got != expected {
		return nil, fmt.Errorf("runtime diagnostic layer differs: got %s, expected %s", got, expected)
	}
	return canonical.Marshal(inventory)
}

func TestRuntimeTarInventory(t *testing.T) {
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for _, header := range []*tar.Header{
		{Name: "./file", Mode: 0o640, Uid: 7, Gid: 8, Size: 4, ModTime: time.Unix(123, 0)},
		{Name: "./alias", Mode: 0o640, Typeflag: tar.TypeLink, Linkname: "./file", ModTime: time.Unix(123, 0)},
	} {
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size != 0 {
			if _, err := archive.Write([]byte("data")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	original := buffer.Bytes()
	for _, tc := range []struct{ name, change string }{
		{name: "content metadata and link order"},
		{name: "changed content", change: "content"},
		{name: "changed padding", change: "padding"},
		{name: "truncated archive", change: "truncate"},
		{name: "canceled", change: "cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Clone(original)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch tc.change {
			case "content":
				raw[512] = 'x'
			case "padding":
				raw = append(raw, 1)
			case "truncate":
				raw = raw[:515]
			case "cancel":
				cancel()
			}
			path := filepath.Join(t.TempDir(), "layer.tar")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := runtimeProofTarInventory(ctx, path, fmt.Sprintf("sha256:%x", sha256.Sum256(original)))
			if tc.change != "" {
				if err == nil {
					t.Fatal("accepted changed layer or canceled context")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{`"path":"./file"`, `"uid":7`, `"gid":8`, `"size":4`, `"mode":416`,
				`"mtime":"1970-01-01T00:02:03Z"`, `"linkname":"./file"`, fmt.Sprintf(`"sha256":"sha256:%x"`, sha256.Sum256([]byte("data")))} {
				if !bytes.Contains(got, []byte(field)) {
					t.Fatalf("missing diagnostic field %s", field)
				}
			}
		})
	}
}
