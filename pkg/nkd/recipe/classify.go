package recipe

// ExtensionSurface classifies a resolved result into exactly one of three surfaces
// (ADR-11 §3), so a downstream consumer knows what KIND of extension it is and the
// trust implication: a fork leaves the publisher's chain.
const (
	SurfacePublishedTuple  = "published-tuple"  // selects an existing published recipe by criteria — an existing identity, no new one
	SurfaceDeclaredOverlay = "declared-overlay" // operator overlays/mixins compose a NEW from-source-reproducible identity, still in the publisher's chain
	SurfaceFork            = "fork"             // a --data catalog whose identity differs from the referenced one — OUTSIDE the publisher's chain
)

// ClassifyExtension emits the extension surface for a resolution (ADR-11 §3). The
// It classifies the resolve-time surface:
//   - a publisher-owned catalog → published-tuple;
//   - a supplied catalog that COMPOSES layers (overlays/mixins) → declared-overlay;
//   - a supplied flat catalog → fork (a distinct catalog outside the publisher's chain).
func ClassifyExtension(fromData, layered bool) string {
	switch {
	case !fromData:
		return SurfacePublishedTuple
	case layered:
		return SurfaceDeclaredOverlay
	default:
		return SurfaceFork
	}
}
