// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package nvkedemo

import "context"

// Linux's existing owned dnsmasq service supplies the native address file.
type nativeAddressOwner struct{}

func admitNativeAddresses(context.Context, []string) error { return nil }
func configureNativeAddresses(*Runner, string) error       { return nil }
func startNativeAddresses(context.Context, Runner, string, nativeBridgeConfig, int, context.CancelCauseFunc) (*nativeAddressOwner, error) {
	return nil, nil
}
func (*nativeAddressOwner) Stop(context.Context) error { return nil }
