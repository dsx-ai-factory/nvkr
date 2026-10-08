// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"

type requiredProducerBinding struct{ id, target, component string }

func requiredProducerBindings(slot requiredOutput) ([]requiredProducerBinding, error) {
	var bindings []requiredProducerBinding
	if slot.ImageIndex == nil {
		for _, component := range slot.Components {
			bindings = append(bindings, requiredProducerBinding{id: "result/" + slot.Name + "/" + component, target: slot.Name, component: component})
		}
		return bindings, nil
	}
	if slot.Kind != OutputOCI {
		return nil, e.New(e.CodeVerifyFailed, "image index requirements on non-image output")
	}
	if err := slot.ImageIndex.ValidateFor(slot.Name, slot.BuildDigest, slot.Components); err != nil {
		return nil, err
	}
	for _, child := range slot.ImageIndex.Children {
		bindings = append(bindings, requiredProducerBinding{id: child.ResultArtifactID, target: child.BuildTarget, component: child.Component})
	}
	return bindings, nil
}
