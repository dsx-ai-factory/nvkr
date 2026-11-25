// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/binary"
)

const (
	// The base header every version has. Version 3 adds the feature fields and the header length, to 104 bytes.
	qcow2HeaderBytes   = 72
	qcow2V3HeaderBytes = 104
	// Incompatible feature bit 2: the guest data lives in a separate file this one object does not carry.
	qcow2IncompatibleExternalDataFile = 1 << 2
)

var qcow2Magic = []byte{'Q', 'F', 'I', 0xfb}

// qcow2Info is what the header of a plain qcow2 image says.
type qcow2Info struct {
	version     int
	virtualSize uint64
	clusterSize uint64
}

func (q qcow2Info) record() map[string]any {
	return map[string]any{"version": q.version, "virtualSizeBytes": q.virtualSize, "clusterSizeBytes": q.clusterSize}
}

// qcow2HeaderLength is how many leading bytes a header parse needs: the version field says whether the feature
// fields follow.
func qcow2HeaderLength(head []byte) int {
	if len(head) >= 8 && binary.BigEndian.Uint32(head[4:8]) == 3 {
		return qcow2V3HeaderBytes
	}
	return qcow2HeaderBytes
}

// parseQCOW2Header returns the facts about a plain qcow2 image, or refuses one the lane does not support.
func parseQCOW2Header(data []byte) (qcow2Info, error) {
	if len(data) < qcow2HeaderBytes {
		return qcow2Info{}, unsupported("the image is %d bytes, too small to hold a qcow2 header", len(data))
	}
	magic := data[0:4]
	version := binary.BigEndian.Uint32(data[4:8])
	backingOffset := binary.BigEndian.Uint64(data[8:16])
	clusterBits := binary.BigEndian.Uint32(data[20:24])
	size := binary.BigEndian.Uint64(data[24:32])
	cryptMethod := binary.BigEndian.Uint32(data[32:36])
	if !bytes.Equal(magic, qcow2Magic) {
		return qcow2Info{}, unsupported("the image does not start with the qcow2 magic")
	}
	if version != 2 && version != 3 {
		return qcow2Info{}, unsupported("qcow2 version %d is not supported", version)
	}
	if backingOffset != 0 {
		return qcow2Info{}, unsupported("the image has a backing file, which the imager cannot resolve")
	}
	if cryptMethod != 0 {
		return qcow2Info{}, unsupported("the image is encrypted at the qcow2 layer (method %d)", cryptMethod)
	}
	if clusterBits < 9 || clusterBits > 21 {
		return qcow2Info{}, unsupported("qcow2 cluster size 2^%d is out of range", clusterBits)
	}
	if size == 0 {
		return qcow2Info{}, unsupported("the image declares no virtual size")
	}
	if version == 3 {
		if len(data) < qcow2V3HeaderBytes {
			return qcow2Info{}, unsupported("the image is %d bytes, too small to hold a version 3 qcow2 header", len(data))
		}
		if binary.BigEndian.Uint64(data[72:80])&qcow2IncompatibleExternalDataFile != 0 {
			return qcow2Info{}, unsupported("the image keeps its data in an external data file, which one published object cannot carry")
		}
	}
	return qcow2Info{version: int(version), virtualSize: size, clusterSize: 1 << clusterBits}, nil
}
