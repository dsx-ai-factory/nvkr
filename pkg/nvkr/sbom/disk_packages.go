// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const DiskInstalledPackages = "installed-packages-v1"
const DiskInstalledPackagesMethod = "disk-installed-packages/v1"

var installedDebName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
var installedDebArch = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var installedDebVendor = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
var installedDebUpstreamVersion = regexp.MustCompile(`^[0-9][A-Za-z0-9.+~-]*$`)
var installedDebRevision = regexp.MustCompile(`^[A-Za-z0-9.+~]+$`)

// DiskPackageDocument projects an issued final-disk inventory, without claiming
// material correspondence or file coverage. Factory authenticates the original
// live Run, named output and selected runtime before packaging this description.
func DiskPackageDocument(build, name string, inventory *DiskInstalledInventory) (*BOM, error) {
	if inventory == nil || inventory.subjectSize <= 0 || !canonical.IsDigest(inventory.runtimeDigest) ||
		!canonical.IsDigest(inventory.inspectorDigest) || !canonical.IsDigest(inventory.codecDigest) {
		return nil, e.New(e.CodeProvenanceMissing, "installed-package SBOM requires an issued sandbox inventory")
	}
	packages, err := checkedInstalledDiskPackages(inventory.packages)
	if err != nil {
		return nil, err
	}
	components := make([]Component, 0, len(packages))
	for _, pkg := range packages {
		if !installedDebName.MatchString(pkg.Name) || pkg.Version == "" || !installedDebArch.MatchString(pkg.Architecture) || strings.Contains(pkg.Version, ":") || !installedDebVendor.MatchString(pkg.Vendor) || pkg.Vendor == "unknown" {
			return nil, e.New(e.CodeProvenanceMissing, "installed Debian package lacks exact name, version, architecture or observed vendor")
		}
		// libguestfs splits Debian Version at the colon and first hyphen. Joining
		// these fields preserves revisions (including further hyphens). Epoch zero
		// is its representation of an omitted epoch, and stays implicit here.
		version := pkg.Version
		if pkg.Release != "" {
			version += "-" + pkg.Release
		}
		if pkg.Epoch != "" && pkg.Epoch != "0" {
			version = pkg.Epoch + ":" + version
		}
		if !validInstalledDebVersion(version) {
			return nil, e.New(e.CodeVerifyFailed, "installed Debian package has an invalid version")
		}
		components = append(components, Component{Type: "library", Name: pkg.Name, Version: version,
			PURL: installedDebPURL(pkg.Vendor, pkg.Name, version, pkg.Architecture), Properties: []Property{
				{Name: diskPackageArchitectureProperty, Value: pkg.Architecture},
				{Name: diskPackageFormatProperty, Value: "deb"},
				{Name: "nvkr:packageVendor", Value: pkg.Vendor},
			}})
	}
	bom, err := NewSubject(build, name, inventory.subjectDigest)
	if err != nil {
		return nil, err
	}
	bom.Metadata.Component.Type = "operating-system"
	bom, err = WithDependencies(bom, components, DiskInstalledPackagesMethod, inventory.inspectorDigest)
	if err != nil {
		return nil, err
	}
	bom.Metadata.Props = append(bom.Metadata.Props,
		Property{Name: "nvkr:diskEvidence", Value: DiskInstalledPackages},
		Property{Name: "nvkr:diskInspection", Value: "installed-packages"},
		Property{Name: "nvkr:diskInspectionRuntimeDigest", Value: inventory.runtimeDigest},
		Property{Name: "nvkr:diskInspectorDigest", Value: inventory.inspectorDigest},
		Property{Name: "nvkr:diskCodecDigest", Value: inventory.codecDigest})
	if inventory.helperName != "" {
		bom.Metadata.Props = append(bom.Metadata.Props,
			Property{Name: "nvkr:diskInspectionHelperBuildDigest", Value: inventory.helper.BuildDigest},
			Property{Name: "nvkr:diskInspectionHelperComponent", Value: inventory.helper.Component},
			Property{Name: "nvkr:diskInspectionHelperDigest", Value: inventory.helper.Digest})
	}
	slices.SortFunc(bom.Metadata.Props, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
	bom, err = ForSubject(bom, inventory.subjectDigest, "build-output", "")
	if err != nil {
		return nil, err
	}
	if err := ValidateDiskInstalledPackages(bom); err != nil {
		return nil, err
	}
	return bom, nil
}

// Debian Policy 5.6.12 splits the revision at the last hyphen. This evidence
// profile also requires the reader's bounded, canonical epoch (zero is implicit).
func validInstalledDebVersion(version string) bool {
	if epoch, rest, found := strings.Cut(version, ":"); found {
		value, err := strconv.ParseUint(epoch, 10, 31)
		if err != nil || value == 0 || strconv.FormatUint(value, 10) != epoch {
			return false
		}
		version = rest
	}
	if separator := strings.LastIndexByte(version, '-'); separator >= 0 {
		if !installedDebRevision.MatchString(version[separator+1:]) {
			return false
		}
		version = version[:separator]
	}
	return installedDebUpstreamVersion.MatchString(version)
}

func installedDebPURL(vendor, name, version, arch string) string {
	escape := func(value string) string { return strings.ReplaceAll(url.QueryEscape(value), "+", "%20") }
	// Purl versions retain the Debian epoch colon and encode literal plus signs.
	return "pkg:deb/" + escape(vendor) + "/" + escape(name) + "@" + strings.ReplaceAll(escape(version), "%3A", ":") + "?arch=" + escape(arch)
}

// ValidateDiskInstalledPackages checks the signed vocabulary; it does not issue
// an inspection receipt or authenticate the document's signer or exact subject.
func ValidateDiskInstalledPackages(bom *BOM) error {
	if err := bom.Validate(); err != nil {
		return err
	}
	props := map[string]string{}
	for _, prop := range bom.Metadata.Props {
		props[prop.Name] = prop.Value
	}
	if len(bom.Components) == 0 || bom.Metadata.Component.Type != "operating-system" ||
		props["nvkr:diskEvidence"] != DiskInstalledPackages || props["nvkr:diskInspection"] != "installed-packages" ||
		props["nvkr:dependencyInspection"] != DiskInstalledPackagesMethod || props["nvkr:evidenceStage"] != "build-output" ||
		props["nvkr:dependencyScannerDigest"] != props["nvkr:diskInspectorDigest"] {
		return e.New(e.CodeProvenanceMissing, "disk package inventory lacks its explicit inspected scope")
	}
	for _, key := range []string{"nvkr:diskInspectionRuntimeDigest", "nvkr:diskInspectorDigest", "nvkr:diskCodecDigest"} {
		if !canonical.IsDigest(props[key]) {
			return e.New(e.CodeProvenanceMissing, "disk package inventory lacks %s", key)
		}
	}
	seen := map[string]bool{}
	for _, component := range bom.Components {
		arch, format, vendor := "", "", ""
		for _, prop := range component.Properties {
			switch prop.Name {
			case diskPackageArchitectureProperty:
				arch = prop.Value
			case diskPackageFormatProperty:
				format = prop.Value
			case "nvkr:packageVendor":
				vendor = prop.Value
			default:
				return e.New(e.CodeVerifyFailed, "disk package inventory contains an unsupported package claim")
			}
		}
		key := component.Name + "\x00" + arch
		if component.Type != "library" || !installedDebName.MatchString(component.Name) || !validInstalledDebVersion(component.Version) ||
			!installedDebArch.MatchString(arch) || !installedDebVendor.MatchString(vendor) || vendor == "unknown" || format != "deb" || len(component.Hashes) != 0 ||
			component.PURL != installedDebPURL(vendor, component.Name, component.Version, arch) || seen[key] {
			return e.New(e.CodeVerifyFailed, "disk package inventory contains an invalid or conflicting Debian identity")
		}
		seen[key] = true
	}
	return nil
}
