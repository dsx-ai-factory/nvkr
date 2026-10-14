// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package target

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianTargetResolution(t *testing.T) {
	defaultTarget := Debian{Distribution: "ubuntu-24.04", Platform: "linux/amd64"}
	consumer := Debian{Distribution: "debian-13", Platform: "linux/arm64"}
	routes := []Debian{
		{Distribution: "debian-13", Platform: "linux/amd64"}, consumer,
		defaultTarget, {Distribution: "ubuntu-24.04", Platform: "linux/arm64"},
	}
	for _, tc := range []struct {
		name     string
		request  Debian
		consumer *Debian
		want     Debian
		invalid  bool
	}{
		{name: "standalone defaults", want: defaultTarget},
		{name: "explicit defaults", request: defaultTarget, want: defaultTarget},
		{name: "standalone architecture", request: Debian{Platform: "linux/arm64"}, want: routes[3]},
		{name: "standalone distribution", request: Debian{Distribution: "debian-13"}, want: routes[0]},
		{name: "consumer defaults", consumer: &consumer, want: consumer},
		{name: "explicit consumer", request: consumer, consumer: &consumer, want: consumer},
		{name: "consumer architecture only", request: Debian{Platform: "linux/arm64"}, consumer: &consumer, want: consumer},
		{name: "consumer architecture conflict", request: Debian{Platform: "linux/amd64"}, consumer: &consumer, invalid: true},
		{name: "consumer distribution conflict", request: Debian{Distribution: "ubuntu-24.04"}, consumer: &consumer, invalid: true},
		{name: "incomplete consumer", consumer: &Debian{Platform: "linux/arm64"}, invalid: true},
		{name: "consumer wildcard", consumer: &Debian{Distribution: "debian-13", Platform: "linux/any"}, invalid: true},
		{name: "unsupported distribution", request: Debian{Distribution: "ubuntu-unknown"}, invalid: true},
		{name: "unsupported architecture", request: Debian{Platform: "linux/riscv64"}, invalid: true},
		{name: "distribution whitespace", request: Debian{Distribution: " ubuntu-24.04"}, invalid: true},
		{name: "distribution wildcard", request: Debian{Distribution: "ubuntu-*"}, invalid: true},
		{name: "distribution control", request: Debian{Distribution: "ubuntu-24.04\nother"}, invalid: true},
		{name: "invalid UTF8 request", request: Debian{Distribution: string([]byte{0xff})}, invalid: true},
		{name: "invalid UTF8 consumer", consumer: &Debian{Distribution: string([]byte{0xff}), Platform: "linux/arm64"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveDebian(tc.request, tc.consumer, routes)
			if tc.invalid {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog || got != (Debian{}) {
					t.Fatalf("resolution = %+v, %v; want invalid catalog and no target", got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("resolution = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestDebianResolutionAdmitsCompleteRouteSetWithoutFallback(t *testing.T) {
	defaultTarget := Debian{Distribution: "ubuntu-24.04", Platform: "linux/amd64"}
	armTarget := Debian{Distribution: "ubuntu-24.04", Platform: "linux/arm64"}
	for _, tc := range []struct {
		name   string
		routes []Debian
	}{
		{"no routes", nil},
		{"duplicate", []Debian{defaultTarget, defaultTarget}},
		{"unsorted", []Debian{armTarget, defaultTarget}},
		{"invalid unselected route", []Debian{defaultTarget, {Distribution: "ubuntu-24.04", Platform: "any"}}},
		{"invalid UTF8 unselected route", []Debian{defaultTarget, {Distribution: string([]byte{0xff}), Platform: "linux/amd64"}}},
		{"no default route", []Debian{armTarget}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveDebian(Debian{}, nil, tc.routes)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog || got != (Debian{}) {
				t.Fatalf("resolution = %+v, %v; want invalid catalog without fallback", got, err)
			}
		})
	}
	got, err := ResolveDebian(Debian{}, nil, []Debian{defaultTarget, armTarget})
	if err != nil {
		t.Fatal(err)
	}
	// The result is a value, not an alias of a mutable caller route/consumer.
	defaultTarget.Platform = "linux/arm64"
	if got.Platform != "linux/amd64" {
		t.Fatal("caller mutation changed the resolved target")
	}
}
