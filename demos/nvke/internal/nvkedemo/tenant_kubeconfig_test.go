// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"strings"
	"testing"
)

func TestTenantKubeconfigPublished(t *testing.T) {
	for _, test := range []struct {
		name, raw          string
		published, invalid bool
	}{
		{"absent", "", false, false},
		{"whitespace", " \n\t", false, false},
		{"null", " null\n", false, false},
		{"empty object", `{}`, false, false},
		{"key awaiting certificate", `{"data":{"breakglass-key":"private-fixture"}}`, false, false},
		{"empty data", `{"data":{}}`, false, false},
		{"null data", `{"data":null}`, false, false},
		{"empty value", `{"data":{"value":""}}`, false, false},
		{"null value", `{"data":{"value":null}}`, false, false},
		{"published", `{"data":{"value":"YQ==","breakglass-key":"private-fixture"}}`, true, false},
		{"malformed JSON", `{"data":private-fixture}`, false, true},
		{"invalid base64", `{"data":{"value":"private-fixture"}}`, false, true},
		{"wrong value type", `{"data":{"value":{"private-fixture":true}}}`, false, true},
		{"wrong data type", `{"data":"private-fixture"}`, false, true},
		{"wrong object type", `["private-fixture"]`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			published, err := tenantKubeconfigPublished([]byte(test.raw))
			if published != test.published || (err != nil) != test.invalid {
				t.Fatalf("published=%v error=%v; want published=%v invalid=%v", published, err, test.published, test.invalid)
			}
			if err != nil && strings.Contains(err.Error(), "private-fixture") {
				t.Fatal("private Secret contents leaked")
			}
		})
	}
}
