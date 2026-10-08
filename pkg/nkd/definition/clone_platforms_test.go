// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestClonePreservesPlatformAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		platforms []string
		valid     bool
	}{
		{"omitted", nil, true}, {"explicit-empty", []string{}, false}, {"dual-form", []string{"linux/amd64"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := indexDefinitionFixture()
			d.Outputs[0].Platform = "linux/amd64"
			d.Outputs[0].Platforms = tc.platforms
			copy := d.Clone()
			if !reflect.DeepEqual(d, copy) {
				t.Fatal("clone changed authored values")
			}
			for _, value := range []Definition{d, copy} {
				err := value.Validate()
				if tc.valid {
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
					t.Fatalf("admission changed: %v", err)
				}
			}
		})
	}
}
