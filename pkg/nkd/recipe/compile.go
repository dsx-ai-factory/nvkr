package recipe

import (
	"sort"
	"strconv"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

// Compile lowers a resolved recipe into the pinned build DAG. Components that
// share the same commit, tree, and vendor-content identity collapse into one fetch
// node; transport locators never affect this sharing key. Each buildable component
// gets a build node depending on it; a single terminal verify node depends on all
// artifact producers (NKD-FR-BLD-001, NKD-FR-BLD-006). Tier-4 exclusions are never
// emitted as build nodes (NKD-FR-RC-026).
func Compile(r *Resolved) (*ir.Graph, error) {
	if r == nil {
		return nil, e.New(e.CodeInvalidCatalog, "cannot compile a nil resolved recipe")
	}
	// The graph's Criteria is its identity frame and lands verbatim in the committed
	// nodes.json. Role and clusterType are composition/delivery axes that recipeDigest
	// deliberately excludes (they never select a build), so carrying them here would make
	// two role-SKUs of one category share a recipeDigest yet emit different nodes.json —
	// an execution document not uniquely determined by the identity it claims. Clear them
	// so nodes.json is identity-determined; SKU/role selection is applied downstream at
	// delivery (FilterByRole / --only), not by mutating the build graph.
	identityFrame := r.Criteria
	identityFrame.Role = ""
	identityFrame.ClusterType = ""
	g := &ir.Graph{
		SchemaVersion: ir.SchemaVersion,
		RecipeID:      r.RecipeID,
		Criteria:      identityFrame,
	}
	if a := r.AICRProfile; a != nil {
		g.AICR = &ir.AICRActivation{
			Service:         a.Service,
			Accelerator:     a.Accelerator,
			OS:              a.OS,
			Intent:          a.Intent,
			Platform:        a.Platform,
			RecipeDigest:    a.Digest,
			GeneratorDigest: a.GeneratorDigest,
		}
	}
	fetchByKey := map[string]string{}
	var verifyNeeds []string // build + pull node IDs the terminal verify waits on

	for _, nc := range r.Components {
		// Prebuilt (no-compile) lane: one pull node acquires a pinned, content-addressed
		// artifact — no fetch, no toolchain, no build. Its Digest is the reproducibility
		// pin (part of recipeDigest); the runner verifies the acquired bytes against it.
		if nc.Prebuilt != nil {
			arch := r.Criteria.Arch
			ref := nc.Prebuilt.URL
			if ref == "" {
				ref = nc.Prebuilt.Image
			}
			pid := "pull-" + nc.Name
			verifyNeeds = append(verifyNeeds, pid)
			g.Nodes = append(g.Nodes, ir.Node{
				ID:           pid,
				Kind:         ir.KindPull,
				Component:    nc.Name,
				Artifact:     renderArch(ref, arch),
				Digest:       nc.Prebuilt.Digests[arch],
				Platform:     "linux/" + arch,
				Network:      ir.NetworkDefault,
				Env:          pullEnv(nc),
				OutputType:   nc.OutputType,
				ArtifactPath: prebuiltArtifactName(nc),
				Version:      catalog.NormalizeVersion(nc.Tag),
			})
			continue
		}
		if !nc.FromSource {
			continue
		}
		arch := r.Criteria.Arch
		// Select the same native image for vendoring and compilation. The fetch receipt
		// binds this exact image+command declaration to the resulting dependency closure.
		vendorImage := nc.ToolchainImage
		if t, ok := nc.ToolchainImages[arch]; ok {
			vendorImage = t
		}
		vendorSpecDigest, err := ir.VendorSpecIdentity(nc.VendorCommand, vendorImage)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "component %q vendor specification", nc.Name)
		}
		// A source pin OR vendor specification change gets a fresh checkout directory
		// and node ID. Two components may intentionally use the same repo+commit with
		// different vendoring commands/images; letting those closures collide would
		// mutate shared input state and make dependency selection order-dependent.
		sourceIdentity, err := canonical.Digest(canonical.KindSourceInput, struct {
			Commit           string `json:"commit"`
			TreeHash         string `json:"treeHash"`
			VendorSpecDigest string `json:"vendorSpecDigest"`
		}{Commit: nc.Commit, TreeHash: nc.TreeHash, VendorSpecDigest: vendorSpecDigest})
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "component %q source identity", nc.Name)
		}
		sourceSuffix := strings.TrimPrefix(sourceIdentity, "sha256:")
		if len(sourceSuffix) != 64 {
			return nil, e.New(e.CodeInternal, "component %q source identity is not a canonical sha256 digest", nc.Name)
		}
		// Retain the complete content address. Truncating it would reintroduce a
		// caller-composable checkout/receipt collision even though the sharing map
		// itself was keyed by the full digest.
		dest := "source-" + sourceSuffix
		key := sourceIdentity
		fid, ok := fetchByKey[key]
		if !ok {
			fid = "fetch-" + dest
			fetchByKey[key] = fid
			g.Nodes = append(g.Nodes, ir.Node{
				ID:               fid,
				Kind:             ir.KindFetch,
				Network:          ir.NetworkDefault,
				Repo:             nc.Repo,
				Commit:           nc.Commit,
				TreeHash:         nc.TreeHash,
				Dest:             dest,
				VendorCmd:        nc.VendorCommand,
				VendorImage:      vendorImage,
				VendorSpecDigest: vendorSpecDigest,
			})
		}
		bid := "build-" + nc.Name
		verifyNeeds = append(verifyNeeds, bid)
		// Native per-arch toolchain (model B): the X-native toolchain image runs on an
		// X platform, so the build is native to arch (no cross-compilation). A multi-arch
		// toolchain (manifest list) needs the Platform below to select its native variant;
		// a per-arch pin (ToolchainImages) already encodes the arch. Falls back to the
		// single arch-agnostic ToolchainImage when no per-arch entry exists.
		toolchain := vendorImage
		builderType := nc.BuilderType
		if builderType == "" {
			builderType = ir.BuilderOCIHermetic
		}
		var flakeRef, flakeOutput, flakeLock string
		buildNetwork := ir.NetworkNone
		if builderType == ir.BuilderBuildKit {
			buildNetwork = ir.NetworkDefault
		}
		if builderType == ir.BuilderNix && nc.Flake != nil {
			flakeRef, flakeOutput, flakeLock = nc.Flake.Ref, nc.Flake.Output, nc.Flake.LockDigest
			if nc.Flake.Image != "" {
				toolchain = nc.Flake.Image // the pinned nix container runs the build
			}
		}
		g.Nodes = append(g.Nodes, ir.Node{
			ID:               bid,
			Kind:             ir.KindBuild,
			Needs:            []string{fid},
			Component:        nc.Name,
			BuilderType:      builderType,
			FlakeRef:         flakeRef,
			FlakeOutput:      flakeOutput,
			FlakeLockDigest:  flakeLock,
			Toolchain:        toolchain,
			Platform:         "linux/" + arch,
			Network:          buildNetwork,
			Env:              buildEnv(nc, r.Criteria),
			Patches:          patchRefs(nc.Patches),
			Command:          renderArchAll(nc.BuildCommand, arch),
			Workdir:          "/src",
			SourceDest:       dest,
			SourceCommit:     nc.Commit,
			SourceTreeHash:   nc.TreeHash,
			VendorSpecDigest: vendorSpecDigest,
			SourceReadOnly:   nc.WriteDir != "", // RO source + isolated WriteDir => parallel-safe
			WriteDir:         nc.WriteDir,
			OutputType:       nc.OutputType,
			ArtifactPath:     renderArch(nc.ArtifactPath, arch),
			Version:          catalog.NormalizeVersion(nc.Tag),
		})
	}

	sort.Strings(verifyNeeds)
	g.Nodes = append(g.Nodes, ir.Node{ID: "verify", Kind: ir.KindVerify, Needs: verifyNeeds})
	g.Normalize()
	if err := g.Validate(); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "compiled recipe %q is invalid", r.RecipeID)
	}
	return g, nil
}

// pullEnv carries the deterministic packaging epoch for a prebuilt component that is
// packaged into a .deb (host-package): SOURCE_DATE_EPOCH from the version pin, so the
// assembled .deb is byte-stable. An acquired oci-image or a raw binary needs no epoch,
// so its pull node stays env-free (keeping recipeDigest sensitive only to what affects
// the artifact).
func pullEnv(nc catalog.NamedComponent) []ir.EnvVar {
	if nc.OutputType != "host-package" {
		return nil
	}
	return []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: strconv.FormatInt(nc.SourceDate, 10)}}
}

// prebuiltArtifactName is the on-disk filename a downloaded binary is written to (the
// component name — the conventional binary name). An acquired oci-image has no local
// file, so it carries no artifact path.
func prebuiltArtifactName(nc catalog.NamedComponent) string {
	if nc.Prebuilt != nil && nc.Prebuilt.Image != "" {
		return ""
	}
	return nc.Name
}

// buildEnv assembles the pinned, allowlisted environment for a build node: the
// reproducibility base plus the component's deterministic version stamps
// (NKD-FR-BLD-012/023). The runc lane is the only CGO case (NKD-FR-BLD-025).
func buildEnv(nc catalog.NamedComponent, c criteria.Criteria) []ir.EnvVar {
	cgo := "0"
	if nc.CGO {
		cgo = "1"
	}
	// versionStamp (extra pinned env like KUBE_GIT_*/CC) goes on first; the pinned
	// reproducibility base is applied second so it is authoritative and cannot be
	// shadowed. The catalog loader also fails closed on a collision (defense in
	// depth), so this ordering only matters for non-reserved keys.
	m := map[string]string{}
	for k, v := range nc.VersionStamp {
		m[k] = v
	}
	for k, v := range map[string]string{
		"SOURCE_DATE_EPOCH": strconv.FormatInt(nc.SourceDate, 10),
		"TZ":                "UTC",
		"LC_ALL":            "C",
		"GOTOOLCHAIN":       "local",
		"GOOS":              "linux",
		"GOARCH":            c.Arch,
		"CGO_ENABLED":       cgo,
		// The reproducibility flag discipline (NKD-FR-BLD-023, NKD-FR-REP-004,
		// NKD-NFR-005), pinned in the IR itself so it is part of recipeDigest and
		// applied uniformly to every Go lane regardless of the toolchain image's own
		// ENV: -trimpath strips absolute build paths; -buildvcs=false suppresses VCS
		// stamping (revision/time/dirty), which would otherwise vary with the .git
		// state of the fetched checkout. GOFLAGS entries only apply to `go` commands
		// that accept them, so make- and gcc-based lanes ignore what they don't use.
		// The -ldflags="-buildid=" strip is carried per-command in the catalog for the
		// direct `go build` lanes (CLI -ldflags would otherwise override a GOFLAGS one).
		"GOFLAGS": "-trimpath -buildvcs=false",
	} {
		m[k] = v
	}
	// Per-arch microarchitecture baseline (deterministic; only the target arch's flag
	// is set): amd64 -> GOAMD64=v1, arm64 -> GOARM64=v8.0. Keeps the env minimal and
	// correct for each arch rather than carrying an amd64-only flag everywhere.
	switch c.Arch {
	case "amd64":
		m["GOAMD64"] = "v1"
	case "arm64":
		m["GOARM64"] = "v8.0"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]ir.EnvVar, 0, len(keys))
	for _, k := range keys {
		env = append(env, ir.EnvVar{Name: k, Value: m[k]})
	}
	return env
}

// renderArch substitutes the {{.Arch}} token (the one per-arch literal a build
// command or artifact path needs under model B — e.g. kubernetes' KUBE_BUILD_PLATFORMS
// and its _output/.../linux/<arch>/ path) with the resolved target arch. It is a total
// literal substitution, not template evaluation: a string without the token is returned
// unchanged, and the catalog loader has already failed closed on any other {{...}} token,
// so a typo can never reach a build as a literal.
func renderArch(s, arch string) string {
	return strings.ReplaceAll(s, "{{.Arch}}", arch)
}

// renderArchAll applies renderArch to each element of a build command.
func renderArchAll(cmd []string, arch string) []string {
	if len(cmd) == 0 {
		return nil
	}
	out := make([]string, len(cmd))
	for i, s := range cmd {
		out[i] = renderArch(s, arch)
	}
	return out
}

// patchRefs converts a component's ordered patch series into IR patch refs,
// preserving order (patches apply in sequence, so order is part of the digest).
func patchRefs(ps []catalog.Patch) []ir.PatchRef {
	if len(ps) == 0 {
		return nil
	}
	out := make([]ir.PatchRef, len(ps))
	for i, p := range ps {
		out[i] = ir.PatchRef{Name: p.Name, SHA256: p.SHA256, Content: p.Content}
	}
	return out
}
