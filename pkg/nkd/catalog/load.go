package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/compose"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/packertransport"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	ociname "github.com/google/go-containerregistry/pkg/name"
	"sigs.k8s.io/yaml"
)

var (
	fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// A toolchain must be pinned by an immutable digest, never a tag. Two digest
	// forms are accepted: a registry repo digest (name@sha256:...) and a bare
	// local image ID (sha256:...), the latter being a content-addressed pin for a
	// locally-built toolchain used by the local engine. A tag (e.g. golang:1.23.3)
	// matches neither and fails closed (NKD-FR-RC-024, NKD-FR-BLD-010).
	digestPinned   = regexp.MustCompile(`(?:@|^)sha256:[0-9a-f]{64}$`)
	knownOutputSet = map[string]bool{
		"host-package": true, "oci-image": true, "disk-image": true, "raw": true, "tarball": true,
	}
	// prebuiltOutputSet constrains a prebuilt component's delivery form: a URL binary
	// becomes a host-package (.deb) or is kept raw; an Image is itself the oci-image.
	prebuiltOutputSet = map[string]bool{"host-package": true, "oci-image": true, "raw": true}
	// reproEnvKeys are the reproducibility/hermeticity variables the compiler owns
	// and pins from criteria/component metadata. A catalog's versionStamp may add
	// extra pinned env (KUBE_GIT_*, CC), but must not resolve to a value that
	// overrides these — that would silently break hermeticity, so it fails closed.
	reproEnvKeys = map[string]bool{
		"SOURCE_DATE_EPOCH": true, "TZ": true, "LC_ALL": true, "GOTOOLCHAIN": true,
		"GOOS": true, "GOARCH": true, "GOAMD64": true, "GOARM64": true, "CGO_ENABLED": true, "GOFLAGS": true,
	}
)

const (
	catalogAPIVersion = "nkd.dev/catalog/v1"
	catalogKind       = "SourceBuildRegistry"
)

// registryDoc is the hand-authored registry as parsed from disk, before sources
// and lock pins are merged in.
type registryDoc struct {
	APIVersion string                  `json:"apiVersion"`
	Kind       string                  `json:"kind"`
	Metadata   Metadata                `json:"metadata"`
	Sources    map[string]Source       `json:"sources"`
	Recipes    []Recipe                `json:"recipes"`
	Components map[string]rawComponent `json:"components"`
}

// rawComponent is a component as authored: it references a source by name and
// carries version-stamp templates, not resolved pins.
type rawComponent struct {
	FromSource      bool               `json:"fromSource"`
	Source          string             `json:"source,omitempty"`
	Roles           []string           `json:"roles,omitempty"`          // node-role affinity (ADR-12 §3); composition, not identity
	BuilderType     string             `json:"builderType,omitempty"`    // oci-hermetic (default) | nix | buildkit-rootless | vm-image (ADR-10)
	ToolchainImage  string             `json:"toolchainImage,omitempty"` // component-specific exact builder image
	Flake           *FlakePin          `json:"flake,omitempty"`          // required iff builderType==nix
	CGO             bool               `json:"cgo,omitempty"`
	OutputType      string             `json:"outputType,omitempty"`
	Disk            *DiskSpec          `json:"disk,omitempty"`
	Packer          *PackerConfig      `json:"packer,omitempty"`
	BuildCommand    []string           `json:"buildCommand,omitempty"`
	Materials       map[string]string  `json:"materials,omitempty"`
	ArtifactPath    string             `json:"artifactPath,omitempty"`
	WriteDir        string             `json:"writeDir,omitempty"`
	VersionStamp    map[string]string  `json:"versionStamp,omitempty"` // Go text/template values
	Patches         []patchDeclaration `json:"patches,omitempty"`
	Prebuilt        *Prebuilt          `json:"prebuilt,omitempty"` // the no-compile lane
	ExclusionReason string             `json:"exclusionReason,omitempty"`

	BuildRequirements *execution.Requirements `json:"buildRequirements,omitempty"` // generic build-step runner request
}

// patchDeclaration is the authored patch shape. It intentionally has no content
// field: patch bytes must come through the catalog DataProvider and match the pin.
type patchDeclaration struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// LoadContext performs a deep cancelable catalog load. Every registry, lock, patch,
// overlay, and mixin read is routed through one context-bound provider with aggregate
// byte and directory-entry ceilings.
func LoadContext(ctx context.Context, p DataProvider) (*Catalog, error) {
	bound, err := BindContext(ctx, p)
	if err != nil {
		return nil, err
	}
	loaded, err := load(bound)
	return loaded, normalizeCatalogContextError(ctx, err, "catalog load")
}

func load(p DataProvider) (*Catalog, error) {
	reg, err := readRegistry(p)
	if err != nil {
		return nil, err
	}
	lock, err := readLock(p)
	if err != nil {
		return nil, err
	}
	if err := validateLockAgainstRegistry(reg, lock, true); err != nil {
		return nil, err
	}

	cat := &Catalog{
		APIVersion: reg.APIVersion,
		Kind:       reg.Kind,
		Metadata:   reg.Metadata,
		Sources:    reg.Sources,
		Components: map[string]Component{},
		Tools:      map[string]ToolPin{},
		Actions:    map[string]ActionPin{},
	}

	for name, pin := range lock.Pins {
		if source := reg.Sources[name]; source.Kind == SourceGitHubAction {
			action := ActionPin{Repo: strings.TrimPrefix(source.Repo, "https://github.com/"), Version: pin.Tag, SHA: pin.Commit}
			if err := action.Validate(); err != nil {
				return nil, err
			}
			cat.Actions[name] = action
		}
		if pin.Tool != nil {
			cat.Tools[name] = *pin.Tool
		}
	}

	// Recipes: fill the k8sVersion axis from a source pin when versionFrom is set,
	// so a bump does not require editing the recipe.
	for _, r := range reg.Recipes {
		if r.VersionFrom != "" {
			pin, ok := lock.Pins[r.VersionFrom]
			if !ok {
				return nil, e.New(e.CodeUnpinnedSource,
					"recipe %q versionFrom source %q has no pin in the lock", r.ID, r.VersionFrom)
			}
			r.Criteria.K8sVersion = NormalizeVersion(pin.Tag)
		}
		cat.Recipes = append(cat.Recipes, r)
	}

	// Components: resolve source + pin into the concrete Component.
	names := make([]string, 0, len(reg.Components))
	for n := range reg.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		c, err := resolveComponent(name, reg.Components[name], reg.Sources, lock)
		if err != nil {
			return nil, err
		}
		if err := resolvePatches(p, name, &c, reg.Components[name].Patches); err != nil {
			return nil, err
		}
		cat.Components[name] = c
	}

	// Optional composition layers (base/overlays/mixins). When present the catalog
	// resolves by composing layers; the flat recipes: block may be empty.
	layers, err := compose.LoadStore(p)
	if err != nil {
		return nil, err
	}
	cat.Layers = layers

	if err := cat.Validate(); err != nil {
		return nil, err
	}
	if err := validateLayerRefs(cat); err != nil {
		return nil, err
	}
	return cat, nil
}

// validateLayerRefs fails closed if any layer (base, overlay, or mixin) references a
// component name absent from the registry — the same "recipe references unknown
// component" guarantee the flat path enforces, extended to the composition graph.
func validateLayerRefs(cat *Catalog) error {
	if cat.Layers == nil {
		return nil
	}
	check := func(where string, refs []compose.ComponentRef) error {
		for _, r := range refs {
			if _, ok := cat.Components[r.Name]; !ok {
				return e.New(e.CodeInvalidCatalog, "%s references unknown component %q", where, r.Name)
			}
		}
		return nil
	}
	if cat.Layers.Base != nil {
		if err := check("base", cat.Layers.Base.Spec.ComponentRefs); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(cat.Layers.Overlays))
	for n := range cat.Layers.Overlays {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := check("overlay "+n, cat.Layers.Overlays[n].Spec.ComponentRefs); err != nil {
			return err
		}
	}
	mnames := make([]string, 0, len(cat.Layers.Mixins))
	for n := range cat.Layers.Mixins {
		mnames = append(mnames, n)
	}
	sort.Strings(mnames)
	for _, n := range mnames {
		if err := check("mixin "+n, cat.Layers.Mixins[n].Spec.ComponentRefs); err != nil {
			return err
		}
	}
	return nil
}

// ComposeComponents maps a composed effective set to concrete NamedComponents: each ref
// resolves to its registry component (fail closed on unknown), with any overrides
// deep-merged onto the component's fields and the result re-validated.
func (cat *Catalog) ComposeComponents(refs []compose.ComponentRef) ([]NamedComponent, error) {
	out := make([]NamedComponent, 0, len(refs))
	for _, ref := range refs {
		c, ok := cat.Components[ref.Name]
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "composed recipe references unknown component %q", ref.Name)
		}
		if len(ref.Overrides) > 0 {
			merged, err := applyComponentOverrides(c, ref.Overrides)
			if err != nil {
				return nil, e.Wrap(e.CodeInvalidCatalog, err, "applying overrides to component %q", ref.Name)
			}
			if err := validateComponent(ref.Name, merged); err != nil {
				return nil, err
			}
			c = merged
		}
		out = append(out, NamedComponent{Name: ref.Name, Component: c})
	}
	return out, nil
}

// applyComponentOverrides deep-merges an overlay's overrides onto a typed Component via
// a JSON round-trip (the Component's json tags), mirroring AICR's deep-merge-onto-
// component-config — a set scalar/slice replaces, a nested map merges.
func applyComponentOverrides(c Component, overrides map[string]any) (Component, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return c, err
	}
	mergeJSONMap(m, overrides)
	raw, err = json.Marshal(m)
	if err != nil {
		return c, err
	}
	// Strict decode so an override key that is not a Component field (a typo, e.g.
	// `outputTyp`, or wrong nesting) fails closed rather than being silently dropped —
	// the same discipline the registry/lock/layer decoders use. The merged map's keys are
	// the Component's own fields (always valid) plus any override-introduced keys, so only
	// a bogus override key trips DisallowUnknownFields. (Map-valued fields like
	// versionStamp still accept arbitrary keys, which is correct.)
	var out Component
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return c, err
	}
	return out, nil
}

// mergeJSONMap deep-merges src into dst: nested maps merge, everything else replaces
// (same semantics as compose.deepMergeMap; kept local so catalog does not depend on an
// unexported compose helper).
func mergeJSONMap(dst, src map[string]any) {
	for k, sv := range src {
		if svMap, ok := sv.(map[string]any); ok {
			if dvMap, ok := dst[k].(map[string]any); ok {
				mergeJSONMap(dvMap, svMap)
				continue
			}
		}
		dst[k] = sv
	}
}

func readRegistry(p DataProvider) (*registryDoc, error) {
	raw, err := p.ReadFile(RegistryFile)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "reading %s from %s", RegistryFile, p.Name())
	}
	return decodeRegistry(raw, fmt.Sprintf("%s from %s", RegistryFile, p.Name()))
}

func decodeRegistry(raw []byte, where string) (*registryDoc, error) {
	var reg registryDoc
	// Strict decode: an unknown or misplaced field is a coded error, never silently
	// dropped. This is what makes a reserved axis written inline in a recipe's
	// criteria: block, or a typo'd axis, fail closed (NKD-FR-RC-012).
	if err := yaml.UnmarshalStrict(raw, &reg); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", where)
	}
	if reg.APIVersion != catalogAPIVersion {
		return nil, e.New(e.CodeInvalidCatalog, "catalog apiVersion %q is unsupported; want exactly %q", reg.APIVersion, catalogAPIVersion)
	}
	if reg.Kind != catalogKind {
		return nil, e.New(e.CodeInvalidCatalog, "catalog kind %q is unsupported; want exactly %q", reg.Kind, catalogKind)
	}
	if err := validateRegistryDeclarations(&reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

// validateRegistryDeclarations protects update-only readers (ReadSources/LoadLock) as
// well as full Load. Those paths must never act on a repo or toolchain declaration
// which only a later component resolution would reject.
func validateRegistryDeclarations(reg *registryDoc) error {
	for _, sourceName := range sortedSourceNames(reg.Sources) {
		if strings.TrimSpace(sourceName) == "" || sourceName != strings.TrimSpace(sourceName) {
			return e.New(e.CodeInvalidCatalog, "catalog source name %q is empty or has surrounding whitespace", sourceName)
		}
		src := reg.Sources[sourceName]
		if err := validateSourceKind(src); err != nil {
			return err
		}
		if src.Tool != nil {
			if err := validateToolSource(*src.Tool); err != nil {
				return err
			}
		}
		if strings.TrimSpace(src.Repo) == "" {
			return e.New(e.CodeInvalidCatalog, "catalog source %q has no repository", sourceName)
		}
		if err := checkSourceRepoScheme(sourceName, src.Repo); err != nil {
			return err
		}
		if src.ToolchainBaseImage != "" && (src.ToolchainImage != "" || len(src.ToolchainImagesByArch) > 0) {
			return e.New(e.CodeInvalidCatalog,
				"catalog source %q declares toolchainBaseImage plus a direct/per-arch toolchain; these are duplicate semantic inputs", sourceName)
		}
		if src.ToolchainImage != "" {
			if err := checkOCIRefScheme(sourceName, "toolchainImage", src.ToolchainImage); err != nil {
				return err
			}
			if !digestPinned.MatchString(src.ToolchainImage) {
				return e.New(e.CodeTagToolchain,
					"catalog source %q toolchainImage %q is not digest-pinned", sourceName, src.ToolchainImage)
			}
		}
		if src.ToolchainBaseImage != "" {
			if err := checkOCIRepository(sourceName, "toolchainBaseImage", src.ToolchainBaseImage); err != nil {
				return err
			}
		}
		archs := make([]string, 0, len(src.ToolchainImagesByArch))
		for arch := range src.ToolchainImagesByArch {
			archs = append(archs, arch)
		}
		sort.Strings(archs)
		for _, arch := range archs {
			ref := src.ToolchainImagesByArch[arch]
			if !criteria.ValidArch(arch) {
				return e.New(e.CodeInvalidCatalog,
					"catalog source %q has a toolchain for unsupported v1 arch %q", sourceName, arch)
			}
			if err := checkOCIRefScheme(sourceName, "toolchainImagesByArch", ref); err != nil {
				return err
			}
			if !digestPinned.MatchString(ref) {
				return e.New(e.CodeTagToolchain,
					"catalog source %q toolchain for arch %q (%q) is not digest-pinned", sourceName, arch, ref)
			}
		}
	}
	return nil
}

func readLock(p DataProvider) (*PinLock, error) {
	raw, err := p.ReadFile(LockFile)
	if err != nil {
		return nil, e.Wrap(e.CodeUnpinnedSource, err, "required %s is missing or unreadable from %s", LockFile, p.Name())
	}
	return decodePinLock(raw, fmt.Sprintf("%s from %s", LockFile, p.Name()))
}

func resolveComponent(name string, raw rawComponent, sources map[string]Source, lock *PinLock) (Component, error) {
	if raw.ToolchainImage != "" && !raw.FromSource {
		return Component{}, e.New(e.CodeInvalidCatalog, "component %q: toolchain override requires a from-source build", name)
	}
	c := Component{
		FromSource:      raw.FromSource,
		Roles:           raw.Roles,
		BuilderType:     raw.BuilderType,
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
		src, ok := sources[raw.Source]
		if !ok {
			return c, e.New(e.CodeInvalidCatalog, "prebuilt component %q references unknown source %q", name, raw.Source)
		}
		pin, ok := lock.Pins[raw.Source]
		if !ok {
			return c, e.New(e.CodeUnpinnedSource,
				"prebuilt component %q: source %q has no pin in the lock (run: nkdctl catalog pin %s)", name, raw.Source, raw.Source)
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
	c.Prebuilt = &Prebuilt{URL: url, Image: img, Digests: digests, Evidence: raw.Prebuilt.Evidence}
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

// Validate enforces the resolved-catalog invariants. It is deterministic:
// components and recipes are checked in sorted order so the first error is stable.
func (cat *Catalog) Validate() error {
	if cat == nil {
		return e.New(e.CodeInvalidCatalog, "catalog is nil")
	}
	for _, pin := range cat.Actions {
		if err := pin.Validate(); err != nil {
			return err
		}
	}
	// A catalog needs at least one way to resolve: flat recipes OR composition layers.
	if len(cat.Recipes) == 0 && cat.Layers == nil {
		return e.New(e.CodeInvalidCatalog, "catalog has no recipes and no composition layers")
	}
	if cat.Layers != nil && len(cat.Layers.Categories()) == 0 {
		return e.New(e.CodeInvalidCatalog,
			"layered catalog has no concrete category recipe patterns; an abstract base or orthogonal overlay cannot resolve by itself")
	}
	if cat.Layers != nil {
		if err := cat.Layers.Validate(); err != nil {
			return err
		}
		layerNames := make([]string, 0, len(cat.Layers.Overlays))
		for name := range cat.Layers.Overlays {
			layerNames = append(layerNames, name)
		}
		sort.Strings(layerNames)
		for _, name := range layerNames {
			profile := cat.Layers.Overlays[name].Spec.AICRProfile
			if profile != nil && profile.Repo != "" {
				if err := checkSourceRepoScheme("overlay "+name+" aicrProfile", profile.Repo); err != nil {
					return err
				}
			}
		}
	}

	names := make([]string, 0, len(cat.Components))
	for name := range cat.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return e.New(e.CodeInvalidCatalog, "component name %q is empty or has surrounding whitespace", name)
		}
		if err := validateComponent(name, cat.Components[name]); err != nil {
			return err
		}
	}
	if err := validateMaterialGraph(cat.Components); err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, r := range cat.Recipes {
		if strings.TrimSpace(r.ID) == "" || r.ID != strings.TrimSpace(r.ID) {
			return e.New(e.CodeInvalidCatalog, "recipe id %q is empty or has surrounding whitespace", r.ID)
		}
		if seen[r.ID] {
			return e.New(e.CodeInvalidCatalog, "duplicate recipe id %q", r.ID)
		}
		seen[r.ID] = true
		if err := criteria.CheckReserved(r.Reserved); err != nil {
			return err
		}
		// Composition is an inert v1 hook: recognized but not yet active. A populated
		// base/overlays/mixins fails closed rather than being silently flattened
		// (NKD-FR-RC-014/015, NKD-CON-017).
		if f := r.compositionFields(); len(f) > 0 {
			return e.New(e.CodeUnsupported,
				"recipe %q uses composition %v, which is a reserved v1 hook: recipes must be flat", r.ID, f)
		}
		if err := r.Criteria.ValidateRecipePattern(); err != nil {
			return err
		}
		// A declared AICR profile must be identity-named and digest-pinned (never a
		// tag), mirroring the toolchain fail-closed discipline (NKD v1 §8.1).
		if r.AICRProfile != nil {
			a := r.AICRProfile
			if r.Criteria.Category != "accelerated-runtime" {
				return e.New(e.CodeInvalidCatalog,
					"recipe %q: aicrProfile is valid only for category accelerated-runtime", r.ID)
			}
			if strings.TrimSpace(a.Identity) == "" || a.Identity != strings.TrimSpace(a.Identity) {
				return e.New(e.CodeInvalidCatalog,
					"recipe %q: aicrProfile identity %q is empty or has surrounding whitespace", r.ID, a.Identity)
			}
			if !canonical.IsDigest(a.Digest) {
				return e.New(e.CodeUnpinnedSource,
					"recipe %q: aicrProfile digest %q is not a bare sha256:<64-hex> pin", r.ID, a.Digest)
			}
			if !canonical.IsDigest(a.GeneratorDigest) {
				return e.New(e.CodeUnpinnedSource,
					"recipe %q: aicrProfile generatorDigest %q is not a bare sha256:<64-hex> pin", r.ID, a.GeneratorDigest)
			}
			if err := criteria.ValidateAICRValues(a.Service, a.Accelerator, a.OS, a.Intent, a.Platform); err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err, "recipe %q aicrProfile", r.ID)
			}
			if !criteria.IsWildcard(r.Criteria.OS) && r.Criteria.OS != a.OS {
				return e.New(e.CodeInvalidCatalog,
					"recipe %q criteria os %q conflicts with aicrProfile os %q", r.ID, r.Criteria.OS, a.OS)
			}
			if a.Repo != "" {
				if err := checkSourceRepoScheme(r.ID+" aicrProfile", a.Repo); err != nil {
					return err
				}
			}
		}
		if len(r.Components) == 0 && r.AICRProfile == nil {
			return e.New(e.CodeInvalidCatalog, "recipe %q references no components and declares no AICR activation", r.ID)
		}
		componentRefs := make(map[string]struct{}, len(r.Components))
		for _, cn := range r.Components {
			if strings.TrimSpace(cn) == "" || cn != strings.TrimSpace(cn) {
				return e.New(e.CodeInvalidCatalog,
					"recipe %q component reference %q is empty or has surrounding whitespace", r.ID, cn)
			}
			if _, duplicate := componentRefs[cn]; duplicate {
				return e.New(e.CodeInvalidCatalog,
					"recipe %q references component %q more than once", r.ID, cn)
			}
			componentRefs[cn] = struct{}{}
			component, ok := cat.Components[cn]
			if !ok {
				return e.New(e.CodeInvalidCatalog, "recipe %q references unknown component %q", r.ID, cn)
			}
			if !criteria.IsWildcard(r.Criteria.Arch) {
				if err := ValidateVMImageHostRoute(cn, component, r.Criteria.Arch); err != nil {
					return e.Wrap(e.CodeInvalidCatalog, err,
						"recipe %q cannot realize component %q", r.ID, cn)
				}
			}
		}
		selected := make([]NamedComponent, 0, len(componentRefs))
		for componentName := range componentRefs {
			selected = append(selected, NamedComponent{Name: componentName, Component: cat.Components[componentName]})
		}
		if err := ValidateMaterialSet(selected); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"recipe %q has an incomplete material set", r.ID)
		}
	}
	return nil
}

// validNodeRoles is the ADR-12 §3 role-affinity domain.
var validNodeRoles = map[string]bool{"control-plane": true, "worker": true, "gpu-worker": true}

func validateVMImageBuildRequirements(name string, requirements *execution.Requirements) error {
	if requirements == nil {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image builder requires generic buildRequirements", name)
	}
	resources := requirements.Resources
	if resources == nil {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require a resource request", name)
	}
	if resources.CPUMillis == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require positive CPU", name)
	}
	if resources.MemoryMiB == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require positive memory", name)
	}
	if resources.EphemeralStorageMiB == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require positive ephemeral storage", name)
	}
	if len(requirements.AllowedHostPlatforms) == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require an allowed host platform", name)
	}
	if len(requirements.Capabilities) != 1 || requirements.Capabilities[0].Name != execution.CapabilityKVM {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require an explicit KVM capability policy", name)
	}
	if requirements.TimeoutSeconds == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require a positive executor timeout", name)
	}
	return nil
}

const (
	maxPackerProjectPathBytes  = 4096
	maxPackerArtifactPathBytes = 4096
	maxPackerBuildNameBytes    = 256
	maxMaterialComponentBytes  = 256
)

func validatePackerConfig(name string, config *PackerConfig) error {
	if !validPackerTransportValue(config.ProjectPath, maxPackerProjectPathBytes) ||
		!validSourceRelativePath(config.ProjectPath) || strings.ContainsAny(config.ProjectPath, "{}") {
		return e.New(e.CodeInvalidCatalog,
			"component %q: packer.projectPath %q is not a clean source-relative path", name, config.ProjectPath)
	}
	if !validPackerTransportValue(config.BuildName, maxPackerBuildNameBytes) ||
		strings.ContainsAny(config.BuildName, ",*?[]\\") {
		return e.New(e.CodeInvalidCatalog,
			"component %q: packer.buildName %q must be a bounded exact Packer build name without list or glob syntax",
			name, config.BuildName)
	}
	variableNames := sortedStringKeys(config.Variables)
	publicVariables := make([]packertransport.Variable, 0, len(variableNames))
	for _, variableName := range variableNames {
		publicVariables = append(publicVariables, packertransport.Variable{
			Name: variableName, Value: config.Variables[variableName],
		})
	}
	if err := packertransport.ValidateVariables([]string{
		config.ArchitectureVariable,
		config.MaterialManifestVariable,
		config.OutputDirectoryVariable,
	}, publicVariables); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err,
			"component %q has invalid Packer variable bindings", name)
	}
	return nil
}

func clonePackerConfig(config *PackerConfig) *PackerConfig {
	if config == nil {
		return nil
	}
	cloned := *config
	cloned.Variables = cloneStringMap(config.Variables)
	return &cloned
}

// validPackerTransportValue enforces only transport-safety properties NKD owns. The
// pinned Packer subprocess remains authoritative for project, build-name, and
// variable semantics; catalog loading deliberately does not reimplement HCL.
func validPackerTransportValue(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) &&
		strings.IndexFunc(value, unicode.IsControl) == -1
}

func validateMaterialBindings(name string, component Component) error {
	if len(component.Materials) == 0 {
		return nil
	}
	if component.BuilderType != BuilderVMImage {
		return e.New(e.CodeInvalidCatalog,
			"component %q: material bindings require builderType vm-image", name)
	}
	boundComponents := make(map[string]string, len(component.Materials))
	for _, binding := range sortedStringKeys(component.Materials) {
		materialComponent := component.Materials[binding]
		if _, err := material.StagingPath(binding); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"component %q has invalid material binding name", name)
		}
		if !validPackerTransportValue(materialComponent, maxMaterialComponentBytes) ||
			materialComponent != strings.TrimSpace(materialComponent) {
			return e.New(e.CodeInvalidCatalog,
				"component %q: material binding %q has invalid component name %q", name, binding, materialComponent)
		}
		if materialComponent == name {
			return e.New(e.CodeInvalidCatalog,
				"component %q: material binding %q refers to itself", name, binding)
		}
		if previous, duplicate := boundComponents[materialComponent]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"component %q: material bindings %q and %q ambiguously select component %q",
				name, previous, binding, materialComponent)
		}
		boundComponents[materialComponent] = binding
	}
	return nil
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ValidateMaterialSet verifies one resolved Recipe's complete component set. It
// validates declarations rather than authenticating artifact bytes; execution must
// bind each edge to a verified producer observation before staging.
func ValidateMaterialSet(components []NamedComponent) error {
	selected := make(map[string]Component, len(components))
	for _, component := range components {
		if strings.TrimSpace(component.Name) == "" || component.Name != strings.TrimSpace(component.Name) {
			return e.New(e.CodeInvalidCatalog,
				"resolved material set has invalid component name %q", component.Name)
		}
		if _, duplicate := selected[component.Name]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"resolved material set repeats component %q", component.Name)
		}
		if err := validateComponent(component.Name, component.Component); err != nil {
			return err
		}
		selected[component.Name] = component.Component
	}
	return validateMaterialGraph(selected)
}

func validateMaterialGraph(components map[string]Component) error {
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, binding := range sortedStringKeys(components[name].Materials) {
			targetName := components[name].Materials[binding]
			target, ok := components[targetName]
			if !ok {
				return e.New(e.CodeInvalidCatalog,
					"component %q material binding %q references unknown component %q", name, binding, targetName)
			}
			if !target.FromSource && target.Prebuilt == nil {
				return e.New(e.CodeInvalidCatalog,
					"component %q material binding %q references excluded component %q", name, binding, targetName)
			}
		}
	}

	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(components))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			return e.New(e.CodeInvalidCatalog, "material dependency cycle includes component %q", name)
		case visited:
			return nil
		}
		state[name] = visiting
		for _, binding := range sortedStringKeys(components[name].Materials) {
			if err := visit(components[name].Materials[binding]); err != nil {
				return err
			}
		}
		state[name] = visited
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// ValidateVMImageHostRoute closes the guest/host relationship only after Recipe
// criteria supplies a concrete guest architecture. KVM acceleration cannot cross
// architectures; preferred and forbidden policies retain their declared TCG route.
func ValidateVMImageHostRoute(name string, component Component, guestArch string) error {
	if component.BuilderType != BuilderVMImage {
		// TODO(execution-class admission): Before any adapter admits non-nil
		// execution requirements, define in ADR-18 whether an ordinary builder's
		// Recipe architecture constrains its runner host, then enforce required-KVM
		// host matching here or in the authenticated Environment matcher. Today only
		// vm-image defines that architecture as a guest/host relationship, and every
		// current adapter rejects non-nil requirements before work or output.
		return nil
	}
	if component.BuildRequirements == nil {
		return e.New(e.CodeInvalidCatalog,
			"vm-image component %q has no build requirements", name)
	}
	policy := execution.CapabilityPolicy("")
	for _, capability := range component.BuildRequirements.Capabilities {
		if capability.Name == execution.CapabilityKVM {
			policy = capability.Policy
			break
		}
	}
	if policy != execution.CapabilityRequired {
		return nil
	}
	expected := execution.HostPlatform("linux/" + guestArch)
	for _, platform := range component.BuildRequirements.AllowedHostPlatforms {
		if platform == expected {
			return nil
		}
	}
	return e.New(e.CodeInvalidCatalog,
		"vm-image component %q requires KVM but has no same-architecture host platform for guest architecture %q",
		name, guestArch)
}

func validateComponent(name string, c Component) error {
	// Role affinity (composition metadata) must name known node roles.
	seenRoles := make(map[string]struct{}, len(c.Roles))
	for _, r := range c.Roles {
		if !validNodeRoles[r] {
			return e.New(e.CodeInvalidCatalog,
				"component %q: role affinity %q is not a known node role (control-plane|worker|gpu-worker)", name, r)
		}
		if _, duplicate := seenRoles[r]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"component %q: role affinity %q appears more than once", name, r)
		}
		seenRoles[r] = struct{}{}
	}
	if c.BuildRequirements != nil {
		if !c.FromSource {
			return e.New(e.CodeInvalidCatalog,
				"component %q: buildRequirements require a from-source component", name)
		}
		if err := c.BuildRequirements.Validate(); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"component %q has invalid buildRequirements", name)
		}
	}
	if c.OutputType == "disk-image" {
		if !c.FromSource {
			return e.New(e.CodeInvalidCatalog, "component %q: disk-image is supported only in the from-source lane", name)
		}
		if c.Disk == nil {
			return e.New(e.CodeInvalidCatalog, "component %q: outputType disk-image requires disk settings", name)
		}
		if c.Disk.Format != "qcow2" || (c.Disk.Encryption != "none" && c.Disk.Encryption != "luks2") {
			return e.New(e.CodeInvalidCatalog,
				"component %q: disk requires format qcow2 and encryption none or luks2", name)
		}
	} else if c.Disk != nil {
		return e.New(e.CodeInvalidCatalog, "component %q: disk settings require outputType disk-image", name)
	}
	if c.Packer != nil && c.BuilderType != BuilderVMImage {
		return e.New(e.CodeInvalidCatalog,
			"component %q: packer producer requires builderType vm-image", name)
	}
	if err := validateMaterialBindings(name, c); err != nil {
		return err
	}
	if c.BuilderType == BuilderVMImage && !c.FromSource {
		return e.New(e.CodeInvalidCatalog, "component %q: vm-image builder requires a from-source component", name)
	}
	// A component occupies exactly one lane: from-source XOR prebuilt XOR exclusion.
	// Declaring more than one (e.g. a prebuilt block plus a leftover exclusionReason, or
	// fromSource plus prebuilt) is contradictory and fails closed — the excluded/ambiguous
	// component must never silently resolve to a build or an acquisition.
	lanes := 0
	if c.FromSource {
		lanes++
	}
	if c.Prebuilt != nil {
		lanes++
	}
	if strings.TrimSpace(c.ExclusionReason) != "" {
		lanes++
	}
	if lanes > 1 {
		return e.New(e.CodeInvalidCatalog,
			"component %q declares more than one lane (from-source, prebuilt, and exclusion are mutually exclusive)", name)
	}
	if c.Prebuilt != nil {
		return validatePrebuilt(name, c)
	}
	if !c.FromSource {
		if strings.TrimSpace(c.ExclusionReason) == "" {
			return e.New(e.CodeInvalidCatalog,
				"component %q is not from-source but declares no exclusionReason", name)
		}
		return nil
	}
	switch c.BuilderType {
	case BuilderBuildKit:
		if c.Flake != nil || c.OutputType != "oci-image" {
			return e.New(e.CodeInvalidCatalog, "component %q: rootless BuildKit requires an OCI image output and no flake", name)
		}
	case "", BuilderOCIHermetic: // empty defaults to oci-hermetic (the loader also fills it)
		if c.Flake != nil {
			return e.New(e.CodeInvalidCatalog,
				"component %q: an oci-hermetic builder must not declare a flake", name)
		}
	case BuilderNix:
		if err := validateFlake(name, c.Flake); err != nil {
			return err
		}
	case BuilderVMImage:
		if c.Flake != nil || c.OutputType != "disk-image" {
			return e.New(e.CodeInvalidCatalog, "component %q: vm-image builder requires a disk-image output and no flake", name)
		}
		if err := validateVMImageBuildRequirements(name, c.BuildRequirements); err != nil {
			return err
		}
		if c.CGO || c.WriteDir != "" {
			return e.New(e.CodeInvalidCatalog,
				"component %q: vm-image builder may not declare CGO or a container writeDir", name)
		}
		commandProducer := len(c.BuildCommand) != 0
		packerProducer := c.Packer != nil
		if commandProducer == packerProducer {
			return e.New(e.CodeInvalidCatalog,
				"component %q: vm-image requires exactly one producer, buildCommand or packer", name)
		}
		if packerProducer {
			if err := validatePackerConfig(name, c.Packer); err != nil {
				return err
			}
		}
	default:
		return e.New(e.CodeInvalidCatalog,
			"component %q: unknown builderType %q (want oci-hermetic, nix, buildkit-rootless, or vm-image)", name, c.BuilderType)
	}
	if c.Repo == "" {
		return e.New(e.CodeUnpinnedSource, "component %q: source has no repo", name)
	}
	if err := checkSourceRepoScheme(name, c.Repo); err != nil {
		return err
	}
	if !fullSHA.MatchString(c.Commit) {
		return e.New(e.CodeUnpinnedSource,
			"component %q: commit %q is not a full 40-hex SHA (unpinned source)", name, c.Commit)
	}
	if !fullSHA.MatchString(c.TreeHash) {
		return e.New(e.CodeUnpinnedSource,
			"component %q: treeHash %q is not a full 40-hex Git tree SHA", name, c.TreeHash)
	}
	// A component must resolve to a digest-pinned toolchain. With per-arch toolchains,
	// every listed arch must be digest-pinned (the fallback ToolchainImage may be
	// empty). Otherwise the single ToolchainImage must be digest-pinned (fail closed on
	// a tag — NKD-FR-RC-024).
	if len(c.ToolchainImages) > 0 {
		archs := make([]string, 0, len(c.ToolchainImages))
		for a := range c.ToolchainImages {
			archs = append(archs, a)
		}
		sort.Strings(archs)
		for _, a := range archs {
			if !criteria.ValidArch(a) {
				return e.New(e.CodeInvalidCatalog,
					"component %q: toolchain map has unsupported v1 arch %q", name, a)
			}
			if err := checkOCIRefScheme(name, "toolchain", c.ToolchainImages[a]); err != nil {
				return err
			}
			if !digestPinned.MatchString(c.ToolchainImages[a]) {
				return e.New(e.CodeTagToolchain,
					"component %q: toolchain for arch %q (%q) is not pinned by @sha256: digest", name, a, c.ToolchainImages[a])
			}
		}
		if c.ToolchainImage != "" && !digestPinned.MatchString(c.ToolchainImage) {
			return e.New(e.CodeTagToolchain,
				"component %q: fallback toolchain %q is not pinned by @sha256: digest", name, c.ToolchainImage)
		}
	} else {
		if err := checkOCIRefScheme(name, "toolchain", c.ToolchainImage); err != nil {
			return err
		}
		if !digestPinned.MatchString(c.ToolchainImage) {
			return e.New(e.CodeTagToolchain,
				"component %q: toolchain %q is not pinned by @sha256: digest", name, c.ToolchainImage)
		}
	}
	// VM-image toolchains execute on the runner host, while Recipe criteria names
	// the guest/output architecture. Every eligible host platform needs an exact
	// toolchain pin unless the scalar fallback covers all routes.
	if c.BuilderType == BuilderVMImage && c.ToolchainImage == "" {
		for _, platform := range c.BuildRequirements.AllowedHostPlatforms {
			hostArch := strings.TrimPrefix(string(platform), "linux/")
			if c.ToolchainImages[hostArch] == "" {
				return e.New(e.CodeUnpinnedSource,
					"component %q: allowed host platform %q has no digest-pinned toolchain route",
					name, platform)
			}
		}
	}
	if len(c.BuildCommand) == 0 && c.Packer == nil {
		return e.New(e.CodeInvalidCatalog, "component %q: empty buildCommand", name)
	}
	for _, s := range c.BuildCommand {
		if err := checkArchTokens(name, "buildCommand", s); err != nil {
			return err
		}
	}
	if c.ArtifactPath == "" {
		return e.New(e.CodeInvalidCatalog, "component %q: missing artifactPath", name)
	}
	if err := checkArchTokens(name, "artifactPath", c.ArtifactPath); err != nil {
		return err
	}
	if c.Packer != nil && (!validPackerTransportValue(c.ArtifactPath, maxPackerArtifactPathBytes) ||
		c.ArtifactPath == "." || !validSourceRelativePath(c.ArtifactPath)) {
		return e.New(e.CodeInvalidCatalog,
			"component %q: direct Packer artifactPath %q is not a clean path relative to the private output directory",
			name, c.ArtifactPath)
	}
	// outputType is a required pin field: it selects the delivery form (and its
	// packager) and CGO lane, so a missing one is fail-closed rather than defaulted
	// (NKD-FR-RC-022).
	if c.OutputType == "" {
		return e.New(e.CodeInvalidCatalog, "component %q: missing outputType", name)
	}
	if !knownOutputSet[c.OutputType] {
		return e.New(e.CodeInvalidCatalog, "component %q: unknown outputType %q", name, c.OutputType)
	}
	if c.SourceDate <= 0 {
		return e.New(e.CodeUnpinnedSource,
			"component %q: missing sourceDateEpoch (must derive from the pinned commit)", name)
	}
	seenPatches := make(map[string]struct{}, len(c.Patches))
	for i, p := range c.Patches {
		if !validPatchPath(p.Name) {
			return e.New(e.CodeInvalidCatalog, "component %q: patch[%d] name %q is not a safe provider-relative path", name, i, p.Name)
		}
		if !canonical.IsDigest(p.SHA256) {
			return e.New(e.CodeUnpinnedSource, "component %q: patch %q digest %q is not pinned by sha256:<64-hex>", name, p.Name, p.SHA256)
		}
		if _, ok := seenPatches[p.Name]; ok {
			return e.New(e.CodeInvalidCatalog, "component %q: patch %q appears more than once", name, p.Name)
		}
		seenPatches[p.Name] = struct{}{}
		got := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(p.Content)))
		if got != p.SHA256 {
			return e.New(e.CodeUnpinnedSource, "component %q: resolved patch %q content does not match %s", name, p.Name, p.SHA256)
		}
	}
	vk := make([]string, 0, len(c.VersionStamp))
	for k := range c.VersionStamp {
		vk = append(vk, k)
	}
	sort.Strings(vk)
	for _, k := range vk {
		if reproEnvKeys[k] {
			return e.New(e.CodeInvalidCatalog,
				"component %q: versionStamp may not override pinned reproducibility var %q", name, k)
		}
	}
	return nil
}

// validatePrebuilt enforces the no-compile lane's fail-closed pins (NKD-FR-BLD-050/051):
// exactly one of URL/Image; a delivery form compatible with the acquisition; every
// declared arch digest a bare sha256:<64-hex> (never a tag); at least one arch pinned;
// and no leftover template token other than {{.Arch}} after version rendering. Arch
// digests are checked in sorted order so the first error is deterministic.
func validatePrebuilt(name string, c Component) error {
	p := c.Prebuilt
	hasURL, hasImg := p.URL != "", p.Image != ""
	if hasURL == hasImg {
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: set exactly one of prebuilt.url or prebuilt.image", name)
	}
	if c.OutputType == "" {
		return e.New(e.CodeInvalidCatalog, "prebuilt component %q: missing outputType", name)
	}
	if !prebuiltOutputSet[c.OutputType] {
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: outputType %q not valid for the prebuilt lane (host-package|oci-image|raw)", name, c.OutputType)
	}
	if hasImg && c.OutputType != "oci-image" {
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: prebuilt.image requires outputType oci-image, got %q", name, c.OutputType)
	}
	if hasURL && c.OutputType == "oci-image" {
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: prebuilt.url cannot deliver an oci-image (use host-package or raw)", name)
	}
	ref := p.URL
	if hasImg {
		ref = p.Image
	}
	if err := checkArchTokens(name, "prebuilt ref", ref); err != nil {
		return err
	}
	// ADR-11 §4 scheme allowlist, per field role: a prebuilt download URL is
	// https-only; a prebuilt image is a bare OCI ref or oci://.
	if hasURL {
		if err := checkPrebuiltURLScheme(name, p.URL); err != nil {
			return err
		}
	} else if err := checkOCIRefScheme(name, "prebuilt.image", p.Image); err != nil {
		return err
	}
	if len(p.Digests) == 0 {
		return e.New(e.CodeUnpinnedSource,
			"prebuilt component %q: no pinned digest (author prebuilt.digests, or run: nkdctl catalog pin-artifacts)", name)
	}
	archs := make([]string, 0, len(p.Digests))
	for a := range p.Digests {
		archs = append(archs, a)
	}
	sort.Strings(archs)
	for _, a := range archs {
		if !canonical.IsDigest(p.Digests[a]) {
			return e.New(e.CodeUnpinnedSource,
				"prebuilt component %q: digest for arch %q (%q) is not pinned by sha256:<64-hex>", name, a, p.Digests[a])
		}
	}
	return validateExternalEvidence(name, p.Evidence)
}

// validateExternalEvidence enforces ADR-13 §2: a prebuilt component must declare its
// external evidence explicitly — either `none: true` or a reference whose bytes are
// pinned by a bare sha256 digest — so acquired evidence is never silently absent, and
// a reference is never unpinned.
func validateExternalEvidence(name string, ev *ExternalEvidence) error {
	if ev == nil {
		return e.New(e.CodeProvenanceMissing,
			"prebuilt component %q: no external evidence declared — set prebuilt.evidence.none:true or a digest-pinned sbomRef/provenanceRef (ADR-13 §2)", name)
	}
	if ev.None {
		if ev.SBOMRef != "" || ev.ProvenanceRef != "" {
			return e.New(e.CodeInvalidCatalog, "prebuilt component %q: evidence.none is mutually exclusive with a ref", name)
		}
		return nil
	}
	if ev.SBOMRef == "" && ev.ProvenanceRef == "" {
		return e.New(e.CodeProvenanceMissing,
			"prebuilt component %q: external evidence has neither none:true nor a ref", name)
	}
	if ev.SBOMRef != "" && !canonical.IsDigest(ev.SBOMDigest) {
		return e.New(e.CodeUnpinnedSource, "prebuilt component %q: sbomRef is not pinned by sbomDigest sha256:<64-hex>", name)
	}
	if ev.ProvenanceRef != "" && !canonical.IsDigest(ev.ProvenanceDigest) {
		return e.New(e.CodeUnpinnedSource, "prebuilt component %q: provenanceRef is not pinned by provenanceDigest sha256:<64-hex>", name)
	}
	return nil
}

// checkArchTokens fails closed on any {{...}} template token other than {{.Arch}} in
// a build string. {{.Arch}} is the single per-arch literal Compile substitutes at
// compile time (model B — e.g. kubernetes' KUBE_BUILD_PLATFORMS and its output path);
// a typo'd or unknown token must not silently reach a build as a literal, mirroring
// the fail-closed toolchain-pinning discipline (NKD-FR-RC-024).
func checkArchTokens(name, field, s string) error {
	if strings.Contains(strings.ReplaceAll(s, "{{.Arch}}", ""), "{{") {
		return e.New(e.CodeInvalidCatalog,
			"component %q: %s %q uses an unknown template token (only {{.Arch}} is supported)", name, field, s)
	}
	return nil
}

// ── standard-interface scheme allowlist (ADR-11 §4) ──────────────────────────
//
// Enforced PER FIELD ROLE, not by one blanket regex: source repositories accept
// https/git/ssh; prebuilt download URLs accept https only; OCI image/toolchain
// refs accept a bare OCI reference or an explicit oci:// prefix (the digest-pinning
// discipline is applied on top). The allowlist errs permissive — add schemes later,
// never silently remove — and a violation fails closed naming the field and scheme.

var (
	sourceRepoSchemes = map[string]bool{"https": true, "git": true, "ssh": true}
	scpLikeRepo       = regexp.MustCompile(`^[^@\s/]+@[^:\s/]+:[^\s]+$`)
)

// uriScheme returns the lowercased scheme of a "scheme://…" reference, or "" for a
// scp-like (git@host:path) or bare reference that carries no explicit scheme.
func uriScheme(ref string) string {
	if i := strings.Index(ref, "://"); i > 0 {
		return strings.ToLower(ref[:i])
	}
	return ""
}

// checkSourceRepoScheme allows https/git/ssh and scp-like git@host:path (no scheme).
func checkSourceRepoScheme(name, repo string) error {
	if s := uriScheme(repo); s != "" {
		if sourceRepoSchemes[s] {
			return nil
		}
		return e.New(e.CodeInvalidCatalog,
			"component %q: source repo scheme %q is not allowed (use https, git, or ssh)", name, s)
	}
	if scpLikeRepo.MatchString(repo) {
		return nil
	}
	return e.New(e.CodeInvalidCatalog,
		"component %q: source repo %q is not an https/git/ssh or scp-like SSH reference", name, repo)
}

// checkPrebuiltURLScheme requires https for a prebuilt binary download.
func checkPrebuiltURLScheme(name, url string) error {
	if s := uriScheme(url); s != "https" {
		got := s
		if got == "" {
			got = "(none)"
		}
		return e.New(e.CodeInvalidCatalog,
			"prebuilt component %q: download url scheme %q is not allowed (https only)", name, got)
	}
	return nil
}

// validateFlake fails closed unless a nix builder's flake is fully pinned (ADR-10 §2):
// a flake ref, a lock digest over the transitive closure, an output attribute, and (if
// present) an OCI-scheme nix image. A tag-pinned or unpinned nix image fails closed.
func validateFlake(name string, f *FlakePin) error {
	if f == nil {
		return e.New(e.CodeUnpinnedSource,
			"nix component %q: builderType nix requires a pinned flake", name)
	}
	if strings.TrimSpace(f.Ref) == "" {
		return e.New(e.CodeUnpinnedSource, "nix component %q: flake.ref is empty", name)
	}
	if !canonical.IsDigest(f.LockDigest) {
		return e.New(e.CodeUnpinnedSource,
			"nix component %q: flake.lockDigest %q is not a bare sha256:<64-hex> closure pin", name, f.LockDigest)
	}
	if strings.TrimSpace(f.Output) == "" {
		return e.New(e.CodeInvalidCatalog, "nix component %q: flake.output is empty", name)
	}
	if f.Image != "" {
		if err := checkOCIRefScheme(name, "flake.image", f.Image); err != nil {
			return err
		}
		if !digestPinned.MatchString(f.Image) {
			return e.New(e.CodeTagToolchain,
				"nix component %q: flake.image %q is not pinned by @sha256: digest", name, f.Image)
		}
	}
	return nil
}

// checkOCIRefScheme allows a bare OCI reference (no scheme) or an explicit oci://.
func checkOCIRefScheme(name, field, ref string) error {
	if ref == "" {
		return nil // the caller emits the field-specific missing-pin error
	}
	if s := uriScheme(ref); s != "" && s != "oci" {
		return e.New(e.CodeInvalidCatalog,
			"component %q: %s ref scheme %q is not allowed (use a bare OCI ref or oci://)", name, field, s)
	}
	normalized := strings.TrimPrefix(ref, "oci://")
	if canonical.IsDigest(normalized) {
		return nil // explicitly supported local content-addressed image ID
	}
	if _, err := ociname.ParseReference(normalized); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err,
			"component %q: %s %q is not a well-formed OCI reference", name, field, ref)
	}
	return nil
}

// checkOCIRepository validates the tag-less repository base used to construct
// `<base>:<resolved-go-version>`. Accepting a tag or digest here would make that
// construction ambiguous (or syntactically invalid) and create two version inputs.
func checkOCIRepository(name, field, ref string) error {
	if err := checkOCIRefScheme(name, field, ref); err != nil {
		return err
	}
	normalized := strings.TrimPrefix(ref, "oci://")
	if _, err := ociname.NewRepository(normalized); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err,
			"component %q: %s %q must be a tag-less OCI repository", name, field, ref)
	}
	return nil
}

// String renders a one-line human summary (used by `catalog list`). It includes
// the digest-pinned toolchain so a reviewer sees the full trust chain — pinned
// source ref AND the toolchain that builds it — at a glance (NKD-FR-INT-042).
func (c Component) String() string {
	if c.Prebuilt != nil {
		ref := c.Prebuilt.URL
		if ref == "" {
			ref = c.Prebuilt.Image
		}
		archs := make([]string, 0, len(c.Prebuilt.Digests))
		for a := range c.Prebuilt.Digests {
			archs = append(archs, a)
		}
		sort.Strings(archs)
		return fmt.Sprintf("prebuilt %s [%s] pinned %v", ref, c.OutputType, archs)
	}
	if !c.FromSource {
		return fmt.Sprintf("excluded (%s)", c.ExclusionReason)
	}
	return fmt.Sprintf("%s @ %s [%s] toolchain %s", c.Repo, c.Commit[:12], c.OutputType, abbrevDigest(c.ToolchainImage))
}

// abbrevDigest shortens a digest-pinned image for display, keeping the name (if
// any) and the first 12 hex of the sha256 (e.g. golang@sha256:fe5d57d3b718).
func abbrevDigest(img string) string {
	i := strings.Index(img, "sha256:")
	if i < 0 {
		return img
	}
	hexStart := i + len("sha256:")
	if len(img) >= hexStart+12 {
		return img[:hexStart+12]
	}
	return img
}
