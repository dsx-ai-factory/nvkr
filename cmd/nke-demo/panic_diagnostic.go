// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime/debug"
	"strconv"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Programming defects are not host-preflight failures. reflect, strconv and
// runtime/debug supply a bounded diagnostic representation; it is text, not
// a codec, authority record or public event. CreateTemp provides exclusive
// mode-0600 files; only a generic error and escaped locator reach the presenter.
// Cancellation must not erase this bounded local recovery diagnostic.
func recoveredPanic(_ context.Context, phase string, value any) error {
	log, err := os.CreateTemp("", "nke-demo-panic-*.log")
	if err != nil {
		return nkderr.New(nkderr.CodeInternal, "%s panicked; could not create the recovery diagnostic", phase)
	}
	valueText := boundedPanicValue(value)
	stack := debug.Stack()
	if len(stack) > 32<<10 {
		stack = append(stack[:32<<10], []byte("\n[stack truncated]")...)
	}
	_, writeErr := fmt.Fprintf(log, "Phase: %s\nPanic: %s\n\n%s", phase, valueText, stack)
	closeErr := log.Close()
	if writeErr != nil || closeErr != nil {
		return nkderr.New(nkderr.CodeInternal, "%s panicked; recovery diagnostic is incomplete at %q", phase, log.Name())
	}
	return nkderr.New(nkderr.CodeInternal, "%s panicked; recovery diagnostics retained at %q", phase, log.Name())
}

// Recovery must not invoke user-defined Format, String or Error methods, or
// traverse arbitrarily large composite values. The standard reflection owner
// exposes primitive values without those callbacks; complex/custom payloads
// retain a type label plus an explicit omission, alongside the actual stack.
func boundedPanicValue(value any) string {
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
	typeName := row.Kind().String()
	typeOf := row.Type()
	if typeOf.Name() != "" {
		typeName = bounded(typeOf.PkgPath(), 1024) + "/" + bounded(typeOf.Name(), 1024)
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

func runProgressWork(ctx context.Context, out io.Writer, work func(context.Context, io.Writer) error) (result error) {
	defer func() {
		if value := recover(); value != nil {
			result = recoveredPanic(ctx, "demo worker", value)
		}
	}()
	return work(ctx, out)
}
