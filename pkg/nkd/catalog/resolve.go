package catalog

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func resolveComponent(name string, raw rawComponent, sources map[string]Source, lock *PinLock) (Component, error) {
	if raw.ToolchainImage != "" && !raw.FromSource {
		return Component{}, e.New(e.CodeInvalidCatalog, "component %q: toolchain override requires a from-source build", name)
	}
	c := Component{
		Helm:            raw.Helm,
		Platforms:       append([]string(nil), raw.Platforms...),
		FromSource:      raw.FromSource,
		BuilderType:     raw.BuilderType,
		BuildNetwork:    raw.BuildNetwork,
		Flake:           raw.Flake,
		CGO:             raw.CGO,
		OutputType:      raw.OutputType,
		Disk:            raw.Disk,
		Packer:          clonePackerConfig(raw.Packer),
		BuildCommand:    raw.BuildCommand,
		Materials:       cloneStringMap(raw.Materials),
		ArtifactPath:    raw.ArtifactPath,
		WriteDir:        raw.WriteDir,
		ExclusionReason: raw.ExclusionReason,

		BuildRequirements: execution.Clone(raw.BuildRequirements),
	}
	// Default the from-source builder to oci-hermetic so existing catalogs need no
	// edit; nix is opt-in per component (ADR-10 §3).
	if raw.FromSource && c.BuilderType == "" {
		c.BuilderType = BuilderOCIHermetic
	}
	// Prebuilt (no-compile) lane: acquire a pinned artifact, don't build. It still
	// references a source for its version pin, so it tracks the same version bumps as a
	// from-source component (a fromSource+prebuilt collision is caught by Validate).
	if raw.Prebuilt != nil {
		src, pin, err := memberVersionPin(name, raw, sources, lock)
		if err != nil {
			return c, err
		}
		return resolvePrebuilt(name, raw, src, pin, lock, c)
	}
	if !raw.FromSource {
		return c, nil
	}
	src, ok := sources[raw.Source]
	if !ok {
		return c, e.New(e.CodeInvalidCatalog, "component %q references unknown source %q", name, raw.Source)
	}
	pin, ok := lock.Pins[raw.Source]
	if !ok {
		return c, e.New(e.CodeUnpinnedSource,
			"component %q: source %q has no pin in the lock (run: nkdctl catalog pin %s)", name, raw.Source, raw.Source)
	}
	c.Repo = src.Repo
	c.VendorCommand = src.VendorCommand
	// Prefer the toolchain resolved into the lock (from .go-version); fall back to
	// the source's explicit digest.
	if pin.ToolchainImage != "" {
		c.ToolchainImage = pin.ToolchainImage
	} else {
		c.ToolchainImage = src.ToolchainImage
	}
	c.ToolchainImages = src.ToolchainImagesByArch
	if raw.ToolchainImage != "" {
		c.ToolchainImage = raw.ToolchainImage
		c.ToolchainImages = nil
	}
	c.Tag = pin.Tag
	c.Commit = pin.Commit
	c.TreeHash = pin.TreeHash
	c.SourceDate = pin.SourceDateEpoch
	stamps, err := renderStamps(raw.VersionStamp, pin)
	if err != nil {
		return c, e.Wrap(e.CodeInvalidCatalog, err, "component %q versionStamp", name)
	}
	c.VersionStamp = stamps
	return c, nil
}

// resolvePatches turns provider-relative patch declarations into verified bytes.
// Resolution happens before Catalog.Validate, so every downstream consumer sees a
// complete Patch value and cannot accidentally defer or skip integrity checking.
func resolvePatches(p DataProvider, component string, c *Component, decls []patchDeclaration) error {
	if len(decls) == 0 {
		return nil
	}
	if !c.FromSource {
		return e.New(e.CodeInvalidCatalog, "component %q: patches are only valid for from-source components", component)
	}
	c.Patches = make([]Patch, 0, len(decls))
	seen := make(map[string]struct{}, len(decls))
	for i, d := range decls {
		if !validPatchPath(d.Name) {
			return e.New(e.CodeInvalidCatalog, "component %q: patch[%d] name %q is not a safe provider-relative path", component, i, d.Name)
		}
		if !canonical.IsDigest(d.SHA256) {
			return e.New(e.CodeUnpinnedSource, "component %q: patch %q digest %q is not pinned by sha256:<64-hex>", component, d.Name, d.SHA256)
		}
		if _, ok := seen[d.Name]; ok {
			return e.New(e.CodeInvalidCatalog, "component %q: patch %q appears more than once", component, d.Name)
		}
		seen[d.Name] = struct{}{}
		content, err := p.ReadFile(d.Name)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "reading patch %q for component %q from %s", d.Name, component, p.Name())
		}
		if !utf8.Valid(content) {
			return e.New(e.CodeInvalidCatalog, "component %q: patch %q is not valid UTF-8", component, d.Name)
		}
		got := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		if got != d.SHA256 {
			return e.New(e.CodeUnpinnedSource, "component %q: patch %q digest mismatch: got %s want %s", component, d.Name, got, d.SHA256)
		}
		c.Patches = append(c.Patches, Patch{Name: d.Name, SHA256: d.SHA256, Content: string(content)})
	}
	return nil
}

func validPatchPath(name string) bool {
	return name != "." && validSourceRelativePath(name)
}

func validSourceRelativePath(name string) bool {
	return fs.ValidPath(name) && !strings.Contains(name, `\`)
}

// resolvePrebuilt resolves a no-compile component: it takes the version pin from the
// referenced source (so a prebuilt artifact tracks the same bump as a from-source
// one), renders the acquisition URL/Image against that pin — leaving the {{.Arch}}
// token for compile — and copies the per-arch digests from the single authoritative
// lock entry.
func resolvePrebuilt(name string, raw rawComponent, src Source, pin Pin, lock *PinLock, c Component) (Component, error) {
	c.Repo = src.Repo
	c.Tag = pin.Tag
	c.Commit = pin.Commit
	c.SourceDate = pin.SourceDateEpoch
	url, err := renderRef("prebuilt.url", raw.Prebuilt.URL, pin)
	if err != nil {
		return c, e.Wrap(e.CodeInvalidCatalog, err, "prebuilt component %q", name)
	}
	img, err := renderRef("prebuilt.image", raw.Prebuilt.Image, pin)
	if err != nil {
		return c, e.Wrap(e.CodeInvalidCatalog, err, "prebuilt component %q", name)
	}
	digests := make(map[string]string, len(lock.Artifacts[name].Digests))
	for a, d := range lock.Artifacts[name].Digests {
		digests[a] = d
	}
	if raw.Prebuilt.Chart != nil {
		url = lock.Artifacts[name].URL
	}
	c.Prebuilt = &Prebuilt{URL: url, Image: img, Digests: digests, Evidence: raw.Prebuilt.Evidence}
	if raw.Prebuilt.Chart != nil && strings.HasPrefix(raw.Prebuilt.Chart.Repository, "oci://") {
		for _, digest := range digests {
			c.Prebuilt.OCIArchive = strings.TrimRight(strings.TrimPrefix(raw.Prebuilt.Chart.Repository, "oci://"), "/") + "/" + raw.Prebuilt.Chart.Name + "@" + digest
			break
		}
		c.Prebuilt.URL = ""
	}
	return c, nil
}

// refData is the template context for rendering a prebuilt acquisition ref against
// the version pin. Arch is the literal passthrough "{{.Arch}}" so the single per-arch
// token survives load and is resolved at compile (renderArch) — mirroring the
// from-source lane, where {{.Arch}} is the one per-arch literal.
type refData struct {
	Tag, ModuleTag, Version, Commit string
	Major, Minor, Patch             string
	Arch                            string
}

// renderRef renders a prebuilt URL/Image template against the pin. Version tokens are
// substituted now; {{.Arch}} passes through untouched. A missing/unknown token fails
// closed (missingkey=error), mirroring versionStamp rendering.
func renderRef(field, tmpl string, pin Pin) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	maj, min, pat := splitVersion(pin.Tag)
	data := refData{
		Tag: pin.Tag, ModuleTag: StripTagPrefix(pin.Tag),
		Version: NormalizeVersion(pin.Tag), Commit: pin.Commit,
		Major: maj, Minor: min, Patch: pat, Arch: "{{.Arch}}",
	}
	t, err := template.New(field).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	return b.String(), nil
}

// stampData is the template context for a component's version stamps. The three tag
// renderings are distinct on purpose, because upstream build systems disagree about
// which one they stamp:
//
//	Tag       cli/v0.1.0-rc.2   exactly what git resolves, module prefix included
//	ModuleTag v0.1.0-rc.2       prefix stripped, leading v RETAINED
//	Version   0.1.0-rc.2        prefix stripped, leading v removed
//
// ModuleTag exists because a monorepo's own Makefile typically strips only the module
// path prefix and stamps the rest verbatim, so reproducing its ldflags byte for byte
// needs the v. Rendering {{.Version}} where upstream stamped ModuleTag silently drops
// the v and yields a binary that reports a different version than upstream's own build.
// Major/Minor/Patch are the prefix-free semantic components. Date is the RFC 3339 UTC
// rendering of the pinned SOURCE_DATE_EPOCH, so a build-date stamp is derived from the
// pin rather than from wall-clock time (which would break reproducibility).
type stampData struct {
	Tag, ModuleTag, Version, Commit, TreeHash string
	Epoch                                     int64
	Date                                      string
	Major, Minor, Patch                       string
}

// renderStamps renders each version-stamp value as a Go text/template against the
// resolved pin, so stamps like KUBE_GIT_COMMIT track the pin without churning the
// hand-authored registry. A missing template key fails closed.
func renderStamps(tmpls map[string]string, pin Pin) (map[string]string, error) {
	if len(tmpls) == 0 {
		return nil, nil
	}
	maj, min, pat := splitVersion(pin.Tag)
	data := stampData{
		Tag: pin.Tag, ModuleTag: StripTagPrefix(pin.Tag),
		Version: NormalizeVersion(pin.Tag), Commit: pin.Commit,
		TreeHash: pin.TreeHash, Epoch: pin.SourceDateEpoch, Date: EpochDate(pin.SourceDateEpoch),
		Major: maj, Minor: min, Patch: pat,
	}
	out := make(map[string]string, len(tmpls))
	for k, v := range tmpls {
		t, err := template.New(k).Option("missingkey=error").Parse(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = b.String()
	}
	return out, nil
}

// StripTagPrefix removes a module-scoped tag prefix from an upstream tag
// ("cli/v0.1.0-rc.2" -> "v0.1.0-rc.2"). A monorepo publishes per-module releases under a
// path prefix; that prefix names WHICH module the tag belongs to, never the component's
// version, so it is stripped everywhere a version is rendered. A root-tagged repo
// ("v1.32.0") is unaffected.
func StripTagPrefix(tag string) string {
	if i := strings.LastIndex(tag, "/"); i >= 0 {
		return tag[i+1:]
	}
	return tag
}

// NormalizeVersion strips a module-scoped tag prefix and a leading v
// (v1.32.0 -> 1.32.0, cli/v0.1.0-rc.2 -> 0.1.0-rc.2).
func NormalizeVersion(tag string) string {
	return strings.TrimPrefix(StripTagPrefix(tag), "v")
}

// EpochDate renders a pinned SOURCE_DATE_EPOCH as an RFC 3339 UTC timestamp. Build-date
// stamps must be derived from the pin, never from wall-clock time, so two builds of the
// same pin agree byte for byte.
func EpochDate(epoch int64) string {
	return time.Unix(epoch, 0).UTC().Format(time.RFC3339)
}

// splitVersion returns the major/minor/patch of a tag as strings, tolerating a
// pre-release or build suffix (v1.32.0-rc.1 -> 1, 32, 0).
func splitVersion(tag string) (maj, min, pat string) {
	v := NormalizeVersion(tag)
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	get := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	return get(0), get(1), get(2)
}
