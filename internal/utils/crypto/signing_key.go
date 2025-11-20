// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package crypto

import (
	"crypto/rand"
)

const (
	DefaultSigningKeyLength = 1024
)

func NewSigningKey() []byte {
	return NewSigningKeyWithLength(DefaultSigningKeyLength)
}

func NewSigningKeyWithLength(length int) []byte {
	key := make([]byte, length)
	if _, err := rand.Read(key); err != nil {
		// NOTE: The default Reader uses operating system APIs that are
		// documented to never return an error on all but legacy Linux systems.
		panic(err)
	}
	return key
}
