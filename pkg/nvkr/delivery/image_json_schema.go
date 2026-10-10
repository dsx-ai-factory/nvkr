// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The stdlib owns JSON parsing and the pinned upstream image types own schema
// spelling. encoding/json v1 otherwise accepts case-folded aliases of struct
// fields. Admit exact schema names before decoding without imposing that rule
// on free maps (labels, annotations, volumes) or opaque extension members.
func validateImageSchemaNames(raw []byte, schema reflect.Type) error {
	for schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	if reflect.PointerTo(schema).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		return nil // Upstream scalar codecs, such as Hash and Time, own their values.
	}
	switch schema.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading image schema object")
		}
		fields := make(map[string]reflect.Type, schema.NumField())
		for i := 0; i < schema.NumField(); i++ {
			field := schema.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			field, exact := fields[key]
			if exact {
				if err := validateImageSchemaNames(object[key], field); err != nil {
					return err
				}
				continue
			}
			for name := range fields {
				if strings.EqualFold(key, name) {
					return e.New(e.CodeVerifyFailed, "image schema member %q must use exact spelling %q", key, name)
				}
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			// Scalars such as descriptor.data ([]byte) are not schema objects.
			if schema.Elem().Kind() != reflect.Struct && schema.Elem().Kind() != reflect.Pointer {
				return nil
			}
			return e.Wrap(e.CodeVerifyFailed, err, "reading image schema array")
		}
		for _, value := range values {
			if err := validateImageSchemaNames(value, schema.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
