// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package assemble owns canonical disk measurement and envelope verification. It
// also owns deterministic OCI and Debian byte constructors used by pipeline and
// factory packaging. These path-based constructors do not carry
// a producer receipt, reopen mutable inputs, and publish without a transaction; they
// cannot authorize attestation, packaging, or release. Qualified packaging fails
// closed until a held-input, crash-safe assembler is implemented.
package assemble

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type tarEntry struct {
	name string
	mode int64
	data []byte
	dir  bool
	link string // when non-empty, a symlink target (recorded verbatim, never followed)
}

// validateExecutableName refuses a name that is not exactly one archive path element,
// before any input is read or output written.
func validateExecutableName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsFunc(name, func(r rune) bool { return r == '/' || unicode.IsControl(r) }) {
		return nvkrerr.New(nvkrerr.CodeInvalidCatalog, "executable name %q is not a single archive path element", name)
	}
	return nil
}

func writeTar(entries []tarEntry, epoch int64) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	mt := time.Unix(epoch, 0).UTC()
	for _, e := range entries {
		h := &tar.Header{
			Name: e.name, Mode: e.mode, ModTime: mt,
			Uid: 0, Gid: 0, Uname: "", Gname: "", Format: tar.FormatGNU,
		}
		switch {
		case e.link != "":
			h.Typeflag = tar.TypeSymlink
			h.Linkname = e.link
		case e.dir:
			h.Typeflag = tar.TypeDir
		default:
			h.Typeflag = tar.TypeReg
			h.Size = int64(len(e.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, fmt.Errorf("tar header %q: %w", e.name, err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(e.data); err != nil {
				return nil, fmt.Errorf("tar member %q: %w", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar: %w", err)
	}
	return buf.Bytes(), nil
}

func gzipBytes(in []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("gzip writer: %w", err)
	}
	gw.ModTime = time.Time{} // omit mtime
	gw.OS = 255              // unknown OS, stable across platforms
	if _, err := gw.Write(in); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("closing gzip: %w", err)
	}
	return buf.Bytes(), nil
}

func tarGzip(entries []tarEntry, epoch int64) ([]byte, error) {
	tarBytes, err := writeTar(entries, epoch)
	if err != nil {
		return nil, err
	}
	return gzipBytes(tarBytes)
}

func sha256hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// OCIImage diagnostically assembles a single-layer OCI image placing the binary at
// /usr/local/bin/<name> with entrypoint set, writes it as an OCI layout under
// outDir, and returns the manifest digest ("sha256:...") — a real image digest.
// arch is the image's target architecture (amd64, arm64), stamped in the config so a
// per-arch build produces a correctly-labeled (and distinctly-digested) image.
//
// This path-based, nontransactional constructor is unqualified. It must
// not consume or produce an authority-bearing artifact.
func OCIImage(binaryPath, name string, epoch int64, arch, outDir string) (string, error) {
	if err := validateExecutableName(name); err != nil {
		return "", err
	}
	bin, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", err
	}
	layerTar, err := writeTar([]tarEntry{
		{name: "usr/", mode: 0o755, dir: true},
		{name: "usr/local/", mode: 0o755, dir: true},
		{name: "usr/local/bin/", mode: 0o755, dir: true},
		{name: "usr/local/bin/" + name, mode: 0o755, data: bin},
	}, epoch)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeInternal, err, "assembling OCI layer for %q", name)
	}
	diffID := "sha256:" + sha256hex(layerTar)
	layerGz, err := gzipBytes(layerTar)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeInternal, err, "compressing OCI layer for %q", name)
	}
	layerDigest := "sha256:" + sha256hex(layerGz)

	created := time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	config := ociConfig{
		Created:      created,
		Architecture: arch,
		OS:           "linux",
		Config:       ociRunConfig{Entrypoint: []string{"/usr/local/bin/" + name}},
		RootFS:       ociRootFS{Type: "layers", DiffIDs: []string{diffID}},
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeInternal, err, "encoding OCI config for %q", name)
	}
	configDigest := "sha256:" + sha256hex(configJSON)

	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        ociDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: configDigest, Size: len(configJSON)},
		Layers:        []ociDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: layerDigest, Size: len(layerGz)}},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeInternal, err, "encoding OCI manifest for %q", name)
	}
	manifestDigest := "sha256:" + sha256hex(manifestJSON)
	index := ociIndex{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []ociDescriptor{{
			MediaType:   "application/vnd.oci.image.manifest.v1+json",
			Digest:      manifestDigest,
			Size:        len(manifestJSON),
			Annotations: map[string]string{"org.opencontainers.image.ref.name": name},
		}},
	}
	indexJSON, err := json.Marshal(index)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeInternal, err, "encoding OCI index for %q", name)
	}

	// Write an OCI image layout so the artifact is real and inspectable.
	blobs := filepath.Join(outDir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		return "", err
	}
	for _, b := range []struct {
		digest string
		data   []byte
	}{{layerDigest, layerGz}, {configDigest, configJSON}, {manifestDigest, manifestJSON}} {
		if err := os.WriteFile(filepath.Join(blobs, b.digest[len("sha256:"):]), b.data, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.json"), indexJSON, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(outDir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		return "", err
	}
	return manifestDigest, nil
}

type ociRunConfig struct {
	Entrypoint []string `json:"Entrypoint"`
}
type ociRootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}
type ociConfig struct {
	Created      string       `json:"created"`
	Architecture string       `json:"architecture"`
	OS           string       `json:"os"`
	Config       ociRunConfig `json:"config"`
	RootFS       ociRootFS    `json:"rootfs"`
}
type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int               `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}
type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}
type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Manifests     []ociDescriptor `json:"manifests"`
}

// Deb diagnostically assembles a deterministic Debian package placing the binary at /usr/bin/<name>,
// writes it to outPath, and returns its sha256 digest. arch is the Debian architecture
// (amd64, arm64) stamped in the control file so a per-arch build is labeled correctly.
//
// This path-based, nontransactional constructor is unqualified. It must
// not consume or produce an authority-bearing artifact.
func Deb(binaryPath, name, version string, epoch int64, arch, outPath string) (string, error) {
	if err := validateExecutableName(name); err != nil {
		return "", err
	}
	bin, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", err
	}
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\n"+
		"Maintainer: NVKR <nvkr@nvidia.invalid>\nSection: admin\nPriority: optional\n"+
		"Description: from-source %s built reproducibly by nvkr\n", name, version, arch, name)

	controlGz, err := tarGzip([]tarEntry{
		{name: "./", mode: 0o755, dir: true},
		{name: "./control", mode: 0o644, data: []byte(control)},
	}, epoch)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeInternal, err, "assembling Debian control archive for %q", name)
	}
	dataGz, err := tarGzip([]tarEntry{
		{name: "./", mode: 0o755, dir: true},
		{name: "./usr/", mode: 0o755, dir: true},
		{name: "./usr/bin/", mode: 0o755, dir: true},
		{name: "./usr/bin/" + name, mode: 0o755, data: bin},
	}, epoch)
	if err != nil {
		return "", nvkrerr.Wrap(nvkrerr.CodeInternal, err, "assembling Debian data archive for %q", name)
	}

	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	writeArMember(&ar, "debian-binary", []byte("2.0\n"), epoch)
	writeArMember(&ar, "control.tar.gz", controlGz, epoch)
	writeArMember(&ar, "data.tar.gz", dataGz, epoch)

	if err := os.WriteFile(outPath, ar.Bytes(), 0o644); err != nil {
		return "", err
	}
	return "sha256:" + sha256hex(ar.Bytes()), nil
}

// writeArMember appends a deterministic ar archive member (uid/gid 0, mtime clamped).
func writeArMember(w *bytes.Buffer, name string, data []byte, epoch int64) {
	fmt.Fprintf(w, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, epoch, 0, 0, "100644", len(data))
	w.Write(data)
	if len(data)%2 == 1 {
		w.WriteByte('\n') // members are 2-byte aligned
	}
}
