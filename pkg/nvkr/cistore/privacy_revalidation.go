// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"strings"
	"sync"
)

const (
	maxAdmissionResponses     = 128
	maxAdmissionResponseBytes = 8 << 20
	maxAdmissionETagBytes     = 1024
)

type admissionResponseKey struct {
	url   string
	token string
}

type admissionResponse struct {
	etag string
	raw  []byte
}

// Responses live only in one operation's backend. Each reuse requires a new
// authenticated conditional GET with the fixed Accept and API-version headers.
// Stored bytes are immutable; callers decode them anew after a matching 304.
type admissionResponses struct {
	mu      sync.Mutex
	entries map[admissionResponseKey]admissionResponse
	bytes   int
}

func (c *admissionResponses) lookup(key admissionResponseKey) (admissionResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	return entry, found
}

func (c *admissionResponses) forget(key admissionResponseKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, found := c.entries[key]; found {
		c.bytes -= len(key.url) + len(key.token) + len(entry.etag) + len(entry.raw)
		delete(c.entries, key)
	}
}

func (c *admissionResponses) remember(key admissionResponseKey, entry admissionResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, found := c.entries[key]; found {
		c.bytes -= len(key.url) + len(key.token) + len(previous.etag) + len(previous.raw)
		delete(c.entries, key)
	}
	size := len(key.url) + len(key.token) + len(entry.etag) + len(entry.raw)
	if key.token == "" || len(c.entries) >= maxAdmissionResponses || size > maxAdmissionResponseBytes-c.bytes {
		return
	}
	if c.entries == nil {
		c.entries = make(map[admissionResponseKey]admissionResponse)
	}
	c.entries[key] = entry
	c.bytes += size
}

func validAdmissionETag(value string) bool {
	if len(value) > maxAdmissionETagBytes {
		return false
	}
	value = strings.TrimPrefix(value, "W/")
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for i := 1; i < len(value)-1; i++ {
		if value[i] < 0x21 || value[i] == '"' || value[i] == 0x7f {
			return false
		}
	}
	return true
}
