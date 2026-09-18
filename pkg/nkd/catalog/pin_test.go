package catalog

import "testing"

func TestParseSemverSkipsPrerelease(t *testing.T) {
	if _, ok := parseSemver("v1.32.0"); !ok {
		t.Error("stable tag must parse")
	}
	for _, bad := range []string{"v1.32.0-rc.1", "v1.32.0-alpha.0", "v1.32", "release-1.32.0", "v1.32.0+build"} {
		if _, ok := parseSemver(bad); ok {
			t.Errorf("%q must not parse as a stable release", bad)
		}
	}
}

func TestConstraintMatches(t *testing.T) {
	v, _ := parseSemver("v1.32.4")
	cases := map[string]bool{
		"1.32.x": true, "1.x": true, "1.32.4": true,
		"1.32.0": false, "1.31.x": false, "2.x": false,
	}
	for constraint, want := range cases {
		if got := constraintMatches(constraint, v); got != want {
			t.Errorf("constraint %q vs 1.32.4: got %v want %v", constraint, got, want)
		}
	}
}

func TestSelectTagNewestStable(t *testing.T) {
	tags := []string{"v1.31.9", "v1.32.0", "v1.32.4", "v1.32.10", "v1.32.11-rc.0", "v1.33.0"}
	got, err := SelectTagForSource(tags, Source{Constraint: "1.32.x"})
	if err != nil {
		t.Fatal(err)
	}
	// newest stable within 1.32.x is 1.32.10 (10 > 4; rc excluded; 1.33.0 out of range).
	if got != "v1.32.10" {
		t.Fatalf("selected %q, want v1.32.10", got)
	}
	if _, err := SelectTagForSource(tags, Source{Constraint: "9.9.x"}); err == nil {
		t.Error("no match must error")
	}
}

func TestSplitVersion(t *testing.T) {
	maj, min, pat := splitVersion("v1.32.0")
	if maj != "1" || min != "32" || pat != "0" {
		t.Fatalf("got %q.%q.%q", maj, min, pat)
	}
	maj, min, _ = splitVersion("v1.32.4-rc.1")
	if maj != "1" || min != "32" {
		t.Fatalf("prerelease split wrong: %q.%q", maj, min)
	}
}
