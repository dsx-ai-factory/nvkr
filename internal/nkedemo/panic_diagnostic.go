// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"reflect"
	"runtime"
	"strconv"
)

// PrivatePanicValueDescription is for bounded private diagnostic files only.
// Its returned payload must never enter public progress or error messages.
// Recovery must not invoke user-defined Format, String or Error methods, or
// traverse arbitrarily large composite values. The standard reflection owner
// exposes primitive values without those callbacks; complex/custom payloads
// retain a type label plus an explicit omission, alongside the actual stack.
func PrivatePanicValueDescription(value any) string {
	if value == nil {
		return "nil"
	}
	bounded := func(text string, limit int) string {
		if len(text) > limit {
			return text[:limit] + " [truncated]"
		}
		return text
	}
	row := reflect.ValueOf(value)
	typeOf := row.Type()
	typeName := bounded(typeOf.String(), 2048)
	// runtime.Error is also implementable by callers. Only the standard
	// runtime's own concrete types may execute Error here; nil pointers remain
	// omitted. These diagnostics stay in the private bounded recovery file.
	concreteType := typeOf
	if concreteType.Kind() == reflect.Pointer {
		concreteType = concreteType.Elem()
	}
	if concreteType.PkgPath() == "runtime" && !(row.Kind() == reflect.Pointer && row.IsNil()) {
		if runtimeError, ok := value.(runtime.Error); ok {
			text := func() (text string) {
				// A caller can construct an incomplete exported runtime error.
				// Its formatting must not turn recovery into another panic.
				defer func() {
					if recover() != nil {
						text = "[runtime error detail unavailable]"
					}
				}()
				return bounded(runtimeError.Error(), 32<<10)
			}()
			return bounded(typeName+": "+text, 32<<10)
		}
	}
	var text string
	switch row.Kind() {
	case reflect.String:
		text = bounded(row.String(), 32<<10)
	case reflect.Bool:
		text = strconv.FormatBool(row.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		text = strconv.FormatInt(row.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		text = strconv.FormatUint(row.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		text = strconv.FormatFloat(row.Float(), 'g', -1, typeOf.Bits())
	case reflect.Complex64, reflect.Complex128:
		text = strconv.FormatComplex(row.Complex(), 'g', -1, typeOf.Bits())
	default:
		text = "[value omitted: composite traversal and custom formatting are disabled during recovery]"
	}
	return bounded(typeName+": "+text, 32<<10)
}
