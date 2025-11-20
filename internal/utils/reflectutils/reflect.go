// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package reflectutils

import (
	"reflect"
)

// UseNonZeroOrDefault returns the input if not effectively zero,
// otherwise returns the default.
func UseNonZeroOrDefault[T any](in T, def T) T {
	zero := reflect.Zero(reflect.TypeOf(in)).Interface()
	isZero := reflect.DeepEqual(in, zero)
	if isZero {
		return def
	}
	return in
}
