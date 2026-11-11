// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package diskinspect owns the inspection helper's bounded observations, without
// catalog resolution, build orchestration, or publication authority.
package diskinspect

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	// MaxPackageReportBytes bounds the complete installed-package XML report.
	MaxPackageReportBytes = 8 << 20
	// MaxInstalledPackages bounds allocation before XML projection.
	MaxInstalledPackages = 16384
)

// InstalledDiskPackage preserves the package-manager fields observed by
// libguestfs. Version, release and epoch remain separate: a guessed normalized
// version would hide the upstream package identity. This is installed metadata,
// not evidence that a staged .deb, OCI archive, or loose file is installed.
type InstalledDiskPackage struct {
	Name         string
	Version      string
	Release      string
	Epoch        string
	Architecture string
	Format       string
}

// XML decoding belongs to encoding/xml. This projection is the documented
// virt-inspector application metadata, not a QCOW2/filesystem/package parser.
func ParseInstalledPackagesXML(raw []byte) ([]InstalledDiskPackage, error) {
	if len(raw) > MaxPackageReportBytes {
		return nil, e.New(e.CodeEnvelopeDecode, "disk installed-package report exceeds its byte budget")
	}
	// Apply the count budget before Decode allocates application slices. XML
	// tokenization remains owned by encoding/xml, including malformed input.
	budget := xml.NewDecoder(bytes.NewReader(raw))
	applications := 0
	for {
		token, err := budget.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, diskXMLFailure(err, "checking disk report resource budget")
		}
		if element, ok := token.(xml.StartElement); ok {
			if element.Name.Space != "" {
				return nil, e.New(e.CodeEnvelopeDecode, "disk inspector report has an unsupported XML namespace")
			}
			if element.Name.Local == "application" {
				applications++
				if applications > MaxInstalledPackages {
					return nil, e.New(e.CodeEnvelopeDecode, "disk installed-package inventory exceeds its package budget")
				}
			}
		}
	}
	var report struct {
		XMLName xml.Name `xml:"operatingsystems"`
		Systems []struct {
			Name   []string `xml:"name"`
			Format []string `xml:"package_format"`
			Apps   []struct {
				Name    []string `xml:"name"`
				Version []string `xml:"version"`
				Release []string `xml:"release"`
				Epoch   []string `xml:"epoch"`
				Arch    []string `xml:"arch"`
			} `xml:"applications>application"`
		} `xml:"operatingsystem"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&report); err != nil {
		return nil, diskXMLFailure(err, "decoding disk installed-package report")
	}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, diskXMLFailure(err, "checking disk report boundary")
		}
		text, ok := token.(xml.CharData)
		if !ok || len(bytes.TrimSpace(text)) != 0 {
			return nil, e.New(e.CodeEnvelopeDecode, "disk inspector returned trailing XML content")
		}
	}
	if len(report.Systems) != 1 {
		return nil, e.New(e.CodeEngineCapability, "disk inspection requires one operating system")
	}
	system := report.Systems[0]
	if len(system.Name) != 1 || system.Name[0] != "linux" || len(system.Format) != 1 || system.Format[0] != "deb" {
		return nil, e.New(e.CodeEngineCapability, "disk installed-package inspection supports Linux Debian-package metadata")
	}
	packages := make([]InstalledDiskPackage, 0, len(system.Apps))
	for _, app := range system.Apps {
		name, err := oneDiskPackageField(app.Name, true)
		if err != nil {
			return nil, err
		}
		fields := []string{name}
		for _, values := range [][]string{app.Version, app.Release, app.Epoch, app.Arch} {
			value, err := oneDiskPackageField(values, false)
			if err != nil {
				return nil, err
			}
			fields = append(fields, value)
		}
		packages = append(packages, InstalledDiskPackage{Name: fields[0], Version: fields[1],
			Release: fields[2], Epoch: fields[3], Architecture: fields[4], Format: system.Format[0]})
	}
	return CheckInstalledPackages(packages)
}

// CheckInstalledPackages is shared by XML projection and host-side helper
// report admission. Parsing in a sandbox does not make its metadata trusted.
func CheckInstalledPackages(packages []InstalledDiskPackage) ([]InstalledDiskPackage, error) {
	if len(packages) == 0 {
		return nil, e.New(e.CodeProvenanceMissing, "disk inspector found no installed packages")
	}
	if len(packages) > MaxInstalledPackages {
		return nil, e.New(e.CodeEnvelopeDecode, "disk installed-package inventory exceeds its package budget")
	}
	for _, pkg := range packages {
		if pkg.Format != "deb" {
			return nil, e.New(e.CodeEngineCapability, "disk inspection supports Debian-package metadata")
		}
		if _, err := oneDiskPackageField([]string{pkg.Name}, true); err != nil {
			return nil, err
		}
		for _, field := range []string{pkg.Version, pkg.Release, pkg.Epoch, pkg.Architecture} {
			if _, err := oneDiskPackageField([]string{field}, false); err != nil {
				return nil, err
			}
		}
	}
	packages = slices.Clone(packages)
	slices.SortFunc(packages, func(a, b InstalledDiskPackage) int {
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		return strings.Compare(a.Architecture, b.Architecture)
	})
	for index := 1; index < len(packages); index++ {
		previous, current := packages[index-1], packages[index]
		if previous.Name == current.Name && previous.Architecture == current.Architecture && previous != current {
			return nil, e.New(e.CodeVerifyFailed, "disk inspector returned conflicting installed package %q", current.Name)
		}
	}
	return slices.Compact(packages), nil
}

// encoding/xml owns parsing. Its errors can include arbitrarily long tag names
// from the report, so expose only a library-issued line number and static phase,
// never the raw error as a wrapped cause that user-facing formatting can print.
func diskXMLFailure(err error, phase string) error {
	var syntax *xml.SyntaxError
	if errors.As(err, &syntax) {
		return e.New(e.CodeEnvelopeDecode, "%s: invalid XML at line %d", phase, syntax.Line)
	}
	return e.New(e.CodeEnvelopeDecode, "%s: invalid inspector XML", phase)
}

func oneDiskPackageField(values []string, required bool) (string, error) {
	if len(values) == 0 && !required {
		return "", nil
	}
	if !required && len(values) == 1 && values[0] == "" {
		return "", nil
	}
	if len(values) != 1 || len(values[0]) > 4096 || !canonicalText(values[0]) {
		return "", e.New(e.CodeEnvelopeDecode, "disk inspector package field is absent, duplicate or non-canonical")
	}
	return values[0], nil
}

func canonicalText(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
