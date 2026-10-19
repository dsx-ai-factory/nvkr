// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package helmarchive validates acquired Helm archive bytes without extracting or rewriting them.
package helmarchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv2 "go.yaml.in/yaml/v2"
	"sigs.k8s.io/yaml"
)

const (
	MaxPackageBytes     = 64 << 20
	maxHelmArchiveBytes = 256 << 20
	maxHelmChartBytes   = 1 << 20
	MaxEntries          = 10000
)

var helmChartName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Identity describes the chart metadata found in the verified archive structure.
// It is not proof of an upstream signature or qualification.
type Identity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "Helm archive inspection canceled")
	}
	return nil
}

// Inspect bounds the compressed and expanded bytes, validates every tar record,
// checks the gzip checksum and returns the unique root chart identity. Identical
// vendored subchart records are allowed; conflicting duplicates and links fail.
func Inspect(ctx context.Context, archive []byte) (Identity, error) {
	var identity Identity
	chartName := ""
	if ctx == nil {
		return Identity{}, e.New(e.CodeUsage, "Helm inspection requires a context")
	}
	if err := contextError(ctx); err != nil {
		return Identity{}, err
	}
	if len(archive) > MaxPackageBytes {
		return Identity{}, e.New(e.CodeVerifyFailed, "Helm package exceeds size bound")
	}
	zipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return Identity{}, e.Wrap(e.CodeVerifyFailed, err, "invalid Helm gzip archive")
	}
	defer func() { _ = zipped.Close() }()
	limited := &io.LimitedReader{R: zipped, N: maxHelmArchiveBytes + 1}
	reader := tar.NewReader(limited)
	seen := Records{}
	found := false
	for count := 0; ; count++ {
		if err := contextError(ctx); err != nil {
			return Identity{}, err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Identity{}, e.Wrap(e.CodeVerifyFailed, err, "invalid Helm tar archive")
		}
		if count >= MaxEntries || limited.N <= 0 {
			return Identity{}, e.New(e.CodeVerifyFailed, "Helm archive exceeds bounds")
		}
		entry := strings.TrimSuffix(header.Name, "/")
		if chartName == "" {
			chartName = strings.Split(entry, "/")[0]
			if !helmChartName.MatchString(chartName) {
				return Identity{}, e.New(e.CodeVerifyFailed, "invalid Helm chart root")
			}
		}
		if entry == "" || path.IsAbs(entry) || path.Clean(entry) != entry || strings.ContainsAny(entry, "\\\x00") || (entry != chartName && !strings.HasPrefix(entry, chartName+"/")) {
			return Identity{}, e.New(e.CodeVerifyFailed, "unsafe Helm archive path %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return Identity{}, e.New(e.CodeVerifyFailed, "Helm archive entry %q is not a regular file or directory", header.Name)
		}
		// PAX sparse files retain TypeReg while tar.Reader synthesizes holes
		// above the expanded-byte limiter. Admit only ordinary file bodies.
		for key := range header.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") {
				return Identity{}, e.New(e.CodeVerifyFailed, "Helm archive entry %q is sparse", header.Name)
			}
		}
		if header.Size < 0 || header.Size > maxHelmArchiveBytes || (header.Typeflag == tar.TypeDir && header.Size != 0) {
			return Identity{}, e.New(e.CodeVerifyFailed, "invalid Helm archive entry size")
		}
		var digest [sha256.Size]byte
		if entry == chartName+"/Chart.yaml" {
			if header.Typeflag != tar.TypeReg || header.Size > maxHelmChartBytes {
				return Identity{}, e.New(e.CodeVerifyFailed, "invalid Helm Chart.yaml type or size")
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				return Identity{}, e.Wrap(e.CodeVerifyFailed, err, "reading Helm Chart.yaml")
			}
			if err := decodeHelmYAML(raw, &identity); err != nil {
				return Identity{}, e.Wrap(e.CodeVerifyFailed, err, "invalid Helm Chart.yaml")
			}
			if identity.Name != chartName || identity.Version == "" {
				return Identity{}, e.New(e.CodeVerifyFailed, "Helm metadata does not match its archive root")
			}
			digest = sha256.Sum256(raw)
			found = true
		} else {
			hash := sha256.New()
			if _, err := io.Copy(hash, reader); err != nil || limited.N <= 0 {
				return Identity{}, e.New(e.CodeVerifyFailed, "Helm archive file is truncated or exceeds expanded size bound: %v", err)
			}
			copy(digest[:], hash.Sum(nil))
		}
		if _, err := seen.Add(chartName, header, digest); err != nil {
			return Identity{}, err
		}
	}
	// Consume gzip through its checksum, also bounding data after the tar terminator.
	if _, err := io.Copy(helmPaddingWriter{}, limited); err != nil || limited.N <= 0 {
		return Identity{}, e.New(e.CodeVerifyFailed, "Helm archive checksum invalid or expanded size exceeds bound: %v", err)
	}
	if !found {
		return Identity{}, e.New(e.CodeVerifyFailed, "Helm package lacks root Chart.yaml")
	}
	return identity, nil
}

// Records applies the shared duplicate-record contract to already path-checked
// tar entries. Callers bound entry counts and bytes, and hash the complete body.
// Its zero value is ready to use. Add reports an identical allowed repetition.
type Records struct{ seen map[string]helmArchiveRecord }

func (records *Records) Add(chartRoot string, header *tar.Header, digest [sha256.Size]byte) (bool, error) {
	entry := strings.TrimSuffix(header.Name, "/")
	previous, repeated := records.seen[entry]
	if repeated {
		if !helmRepeatedSubchartFile(chartRoot, entry) || header.Typeflag != tar.TypeReg ||
			previous.header.Typeflag != tar.TypeReg || !reflect.DeepEqual(previous.header, header) || digest != previous.digest {
			return false, e.New(e.CodeVerifyFailed, "duplicate Helm archive path %q is not an identical regular subchart record", header.Name)
		}
		return true, nil
	}
	if records.seen == nil {
		records.seen = make(map[string]helmArchiveRecord)
	}
	records.seen[entry] = helmArchiveRecord{header: header, digest: digest}
	return false, nil
}

type helmArchiveRecord struct {
	header *tar.Header
	digest [sha256.Size]byte
}

// Some published charts repeat vendored subchart records. Only regular files
// below charts/<subchart>/ may repeat, with all decoded tar metadata and payload
// bytes identical. Root metadata, directories, and other chart files stay unique.
// Every repeated record still consumes the ordinary entry and expanded-byte limits.
func helmRepeatedSubchartFile(chartName, entry string) bool {
	relative, ok := strings.CutPrefix(entry, chartName+"/charts/")
	if !ok {
		return false
	}
	subchart, file, ok := strings.Cut(relative, "/")
	return ok && file != "" && helmChartName.MatchString(subchart)
}

type helmPaddingWriter struct{}

func (helmPaddingWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != 0 {
			return 0, fmt.Errorf("non-padding data after Helm tar terminator")
		}
	}
	return len(p), nil
}

func decodeHelmYAML(raw []byte, target any) error {
	decoder := yamlv2.NewDecoder(bytes.NewReader(raw))
	decoder.SetStrict(true)
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return err
	}
	if err := decoder.Decode(&doc); err != io.EOF {
		return fmt.Errorf("expected exactly one YAML document")
	}
	encoded, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}
