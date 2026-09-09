package compose

import (
	"path"
	"sort"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// Catalog directory layout: base + overlays live under overlays/, mixins
// under mixins/. A catalog that ships neither is flat-only (LoadStore returns nil, nil).
const (
	overlaysDir = "overlays"
	mixinsDir   = "mixins"
	baseFile    = "base.yaml"
)

// FileProvider is the minimal read surface the loader needs; catalog.DataProvider
// satisfies it structurally, so compose stays decoupled from the catalog package.
type FileProvider interface {
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]string, error)
}

// header is the kind/apiVersion/name probe decoded before the full document, so a
// non-recipe file (or a mixin among overlays) is classified and validated by kind
// before a strict full decode (mirrors AICR buildMetadataStore's two-step decode).
type header struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

// LoadStore builds a composition Store from a provider's overlays/ and mixins/ dirs. It
// is fail-closed (mirrors AICR buildMetadataStore): a wrong kind, a duplicate name, a
// parse error, or overlays present without a base.yaml is a coded error. A catalog with
// no overlays/ dir is flat-only and yields (nil, nil) so the caller keeps the flat path.
func LoadStore(p FileProvider) (*Store, error) {
	overlayFiles, err := p.ReadDir(overlaysDir)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "listing %s/", overlaysDir)
	}
	if len(overlayFiles) == 0 {
		return nil, nil // flat-only catalog
	}

	st := &Store{Overlays: map[string]*RecipeMetadata{}, Mixins: map[string]*RecipeMixin{}}

	sort.Strings(overlayFiles)
	for _, f := range overlayFiles {
		if !isYAML(f) {
			continue
		}
		rel := path.Join(overlaysDir, f)
		raw, err := p.ReadFile(rel)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "reading %s", rel)
		}
		var h header
		if err := yaml.Unmarshal(raw, &h); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", rel)
		}
		if h.Kind != RecipeMetadataKind {
			return nil, e.New(e.CodeInvalidCatalog, "%s: kind %q, want %q", rel, h.Kind, RecipeMetadataKind)
		}
		if h.APIVersion != APIVersion {
			return nil, e.New(e.CodeInvalidCatalog, "%s: apiVersion %q, want %q", rel, h.APIVersion, APIVersion)
		}
		var rm RecipeMetadata
		if err := yaml.UnmarshalStrict(raw, &rm); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", rel)
		}
		// base.yaml is the root; its metadata.name must be empty or exactly "base" (a
		// different name would make it addressable under two identities — "base" via the
		// seed and its own name via getByName — so it fails closed rather than aliasing).
		// Classified by name-without-extension so base.yml is recognized too (isYAML
		// accepts both), not misfiled as an overlay named "base".
		if isBaseFile(f) {
			if st.Base != nil {
				return nil, e.New(e.CodeInvalidCatalog, "duplicate base recipe (%s)", rel)
			}
			if rm.Metadata.Name != "" && rm.Metadata.Name != baseRecipeName {
				return nil, e.New(e.CodeInvalidCatalog,
					"%s: base recipe metadata.name must be %q or empty, got %q", rel, baseRecipeName, rm.Metadata.Name)
			}
			rm.Metadata.Name = baseRecipeName
			st.Base = &rm
			continue
		}
		if rm.Metadata.Name == "" {
			return nil, e.New(e.CodeInvalidCatalog, "%s: overlay has no metadata.name", rel)
		}
		if rm.Metadata.Name == baseRecipeName {
			return nil, e.New(e.CodeInvalidCatalog, "%s: overlay name %q is reserved for base.yaml", rel, baseRecipeName)
		}
		if _, dup := st.Overlays[rm.Metadata.Name]; dup {
			return nil, e.New(e.CodeInvalidCatalog, "duplicate overlay name %q (%s)", rm.Metadata.Name, rel)
		}
		if rm.Spec.Criteria == nil {
			return nil, e.New(e.CodeInvalidCatalog, "%s: overlay %q declares no criteria", rel, rm.Metadata.Name)
		}
		st.Overlays[rm.Metadata.Name] = &rm
	}

	if st.Base == nil {
		return nil, e.New(e.CodeInvalidCatalog, "layered catalog has %s/ but no %s/%s", overlaysDir, overlaysDir, baseFile)
	}

	mixinFiles, err := p.ReadDir(mixinsDir)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "listing %s/", mixinsDir)
	}
	sort.Strings(mixinFiles)
	for _, f := range mixinFiles {
		if !isYAML(f) {
			continue
		}
		rel := path.Join(mixinsDir, f)
		raw, err := p.ReadFile(rel)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "reading %s", rel)
		}
		var h header
		if err := yaml.Unmarshal(raw, &h); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", rel)
		}
		if h.Kind != RecipeMixinKind {
			return nil, e.New(e.CodeInvalidCatalog, "%s: kind %q, want %q", rel, h.Kind, RecipeMixinKind)
		}
		if h.APIVersion != APIVersion {
			return nil, e.New(e.CodeInvalidCatalog, "%s: apiVersion %q, want %q", rel, h.APIVersion, APIVersion)
		}
		var mx RecipeMixin
		if err := yaml.UnmarshalStrict(raw, &mx); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", rel)
		}
		if mx.Metadata.Name == "" {
			return nil, e.New(e.CodeInvalidCatalog, "%s: mixin has no metadata.name", rel)
		}
		if _, dup := st.Mixins[mx.Metadata.Name]; dup {
			return nil, e.New(e.CodeInvalidCatalog, "duplicate mixin name %q (%s)", mx.Metadata.Name, rel)
		}
		st.Mixins[mx.Metadata.Name] = &mx
	}

	if err := st.Validate(); err != nil {
		return nil, err
	}
	return st, nil
}

func isYAML(f string) bool { return strings.HasSuffix(f, ".yaml") || strings.HasSuffix(f, ".yml") }

// isBaseFile matches the root document by basename-without-extension, so both base.yaml
// and base.yml (isYAML accepts either) are classified as the base rather than misfiled
// as an overlay named "base".
func isBaseFile(f string) bool {
	f = strings.TrimSuffix(f, ".yaml")
	f = strings.TrimSuffix(f, ".yml")
	return f == "base"
}

// Validate checks static store integrity independent of any query (mirrors AICR's
// post-load base-dependency + reference checks): every overlay's spec.base resolves to
// base or another overlay with no cycle, and every referenced mixin exists.
func (s *Store) Validate() error {
	if s == nil {
		return e.New(e.CodeInvalidCatalog, "composition store is nil")
	}
	if s.Base != nil {
		if s.Base.Spec.Criteria != nil {
			return e.New(e.CodeInvalidCatalog,
				"base criteria must be absent; the base is applied unconditionally")
		}
		if s.Base.Spec.Base != "" {
			return e.New(e.CodeInvalidCatalog,
				"base spec.base must be empty; the base is the composition root")
		}
		if s.Base.Spec.AICRProfile != nil {
			return e.New(e.CodeInvalidCatalog,
				"base cannot declare an AICR profile; profiles belong to accelerated-runtime recipe overlays")
		}
	}
	mixinNames := make([]string, 0, len(s.Mixins))
	for name := range s.Mixins {
		mixinNames = append(mixinNames, name)
	}
	sort.Strings(mixinNames)
	for _, name := range mixinNames {
		mixin := s.Mixins[name]
		probe := &Spec{}
		if err := probe.Merge(&Spec{Constraints: mixin.Spec.Constraints, ComponentRefs: mixin.Spec.ComponentRefs}); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "validating mixin %q", name)
		}
	}
	names := make([]string, 0, len(s.Overlays))
	for n := range s.Overlays {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		o := s.Overlays[n]
		if err := validateLayerCriteria("overlay "+n, o.Spec.Criteria); err != nil {
			return err
		}
		if profile := o.Spec.AICRProfile; profile != nil {
			if o.Spec.Criteria == nil || o.Spec.Criteria.Category != "accelerated-runtime" {
				return e.New(e.CodeInvalidCatalog,
					"overlay %q: aicrProfile is valid only on an accelerated-runtime recipe layer", n)
			}
			if err := profile.Validate(); err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err, "overlay %q aicrProfile", n)
			}
			if !criteria.IsWildcard(o.Spec.Criteria.OS) && o.Spec.Criteria.OS != profile.OS {
				return e.New(e.CodeInvalidCatalog,
					"overlay %q criteria os %q conflicts with aicrProfile os %q",
					n, o.Spec.Criteria.OS, profile.OS)
			}
		}
		// base chain resolves (walks parents, detects cycles + unknown base)
		chain, err := s.resolveInheritanceChain(n)
		if err != nil {
			return err
		}
		// Exercise the same authoritative merge used at resolution time so every
		// latent ancestor constraint collision fails when the store loads, even if a
		// particular query never happens to select this overlay.
		probe := &Spec{}
		for _, layer := range chain {
			if err := probe.Merge(&layer.Spec); err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err,
					"validating inheritance chain for overlay %q", n)
			}
		}
		if err := s.mergeMixins(probe); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"validating mixins for overlay %q", n)
		}
		for _, m := range o.Spec.Mixins {
			if _, ok := s.Mixins[m]; !ok {
				return e.New(e.CodeInvalidCatalog, "overlay %q references unknown mixin %q", n, m)
			}
		}
	}
	// base's own mixins must resolve too.
	if s.Base != nil {
		probe := &Spec{}
		if err := probe.Merge(&s.Base.Spec); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "validating base constraints")
		}
		if err := s.mergeMixins(probe); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "validating base mixins")
		}
		for _, m := range s.Base.Spec.Mixins {
			if _, ok := s.Mixins[m]; !ok {
				return e.New(e.CodeInvalidCatalog, "base references unknown mixin %q", m)
			}
		}
	}
	return nil
}

func validateLayerCriteria(where string, pattern *criteria.Criteria) error {
	if pattern == nil {
		return nil
	}
	if err := pattern.ValidateRecipePattern(); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "%s criteria", where)
	}
	return nil
}
