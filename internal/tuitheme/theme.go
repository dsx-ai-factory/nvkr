// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package tuitheme supplies presentation colors shared by the NVKR and NVKE
// demo dashboards. It contains no navigation or execution policy.
package tuitheme

import "github.com/gdamore/tcell/v2"

// Palette defines only the shared visual frame, not the meaning of an outcome.
type Palette struct {
	Background, Foreground, Accent, Border, Warning, Failure tcell.Color
}

// Colors preserves terminal defaults for monochrome presentation.
func Colors(monochrome bool) Palette {
	if monochrome {
		return Palette{tcell.ColorDefault, tcell.ColorDefault, tcell.ColorDefault, tcell.ColorDefault, tcell.ColorDefault, tcell.ColorDefault}
	}
	return Palette{
		Background: tcell.NewHexColor(0x1c2633), Foreground: tcell.ColorLightSteelBlue,
		Accent: tcell.ColorLightCyan, Border: tcell.NewHexColor(0x52657a),
		Warning: tcell.ColorGoldenrod, Failure: tcell.ColorLightCoral,
	}
}
