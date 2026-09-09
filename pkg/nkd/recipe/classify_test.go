package recipe

import "testing"

func TestClassifyExtension(t *testing.T) {
	cases := []struct {
		fromData, layered bool
		want              string
	}{
		{false, false, SurfacePublishedTuple},
		{false, true, SurfacePublishedTuple}, // publisher-owned catalog
		{true, true, SurfaceDeclaredOverlay},
		{true, false, SurfaceFork},
	}
	for _, tc := range cases {
		if got := ClassifyExtension(tc.fromData, tc.layered); got != tc.want {
			t.Errorf("ClassifyExtension(%v,%v)=%q want %q", tc.fromData, tc.layered, got, tc.want)
		}
	}
}
