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
	"time"
)

type tarEntry struct {
	name string
	mode int64
	data []byte
	dir  bool
	link string // when non-empty, a symlink target (recorded verbatim, never followed)
}

func writeTar(entries []tarEntry, epoch int64) []byte {
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
		_ = tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write(e.data)
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

func gzipBytes(in []byte) []byte {
	var buf bytes.Buffer
	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	gw.ModTime = time.Time{} // omit mtime
	gw.OS = 255              // unknown OS, stable across platforms
	_, _ = gw.Write(in)
	_ = gw.Close()
	return buf.Bytes()
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
	bin, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", err
	}
	layerTar := writeTar([]tarEntry{
		{name: "usr/", mode: 0o755, dir: true},
		{name: "usr/local/", mode: 0o755, dir: true},
		{name: "usr/local/bin/", mode: 0o755, dir: true},
		{name: "usr/local/bin/" + name, mode: 0o755, data: bin},
	}, epoch)
	diffID := "sha256:" + sha256hex(layerTar)
	layerGz := gzipBytes(layerTar)
	layerDigest := "sha256:" + sha256hex(layerGz)

	created := time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	config := ociConfig{
		Created:      created,
		Architecture: arch,
		OS:           "linux",
		Config:       ociRunConfig{Entrypoint: []string{"/usr/local/bin/" + name}},
		RootFS:       ociRootFS{Type: "layers", DiffIDs: []string{diffID}},
	}
	configJSON, _ := json.Marshal(config)
	configDigest := "sha256:" + sha256hex(configJSON)

	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        ociDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: configDigest, Size: len(configJSON)},
		Layers:        []ociDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: layerDigest, Size: len(layerGz)}},
	}
	manifestJSON, _ := json.Marshal(manifest)
	manifestDigest := "sha256:" + sha256hex(manifestJSON)

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
	indexJSON, _ := json.Marshal(index)
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
	bin, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", err
	}
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\n"+
		"Maintainer: NVKR <nvkr@nvidia.invalid>\nSection: admin\nPriority: optional\n"+
		"Description: from-source %s built reproducibly by nvkr\n", name, version, arch, name)

	controlGz := gzipBytes(writeTar([]tarEntry{
		{name: "./", mode: 0o755, dir: true},
		{name: "./control", mode: 0o644, data: []byte(control)},
	}, epoch))
	dataGz := gzipBytes(writeTar([]tarEntry{
		{name: "./", mode: 0o755, dir: true},
		{name: "./usr/", mode: 0o755, dir: true},
		{name: "./usr/bin/", mode: 0o755, dir: true},
		{name: "./usr/bin/" + name, mode: 0o755, data: bin},
	}, epoch))

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
