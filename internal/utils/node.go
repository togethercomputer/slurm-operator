// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/togethercomputer/slurm-operator/internal/annotations"
)

type NodeByWeight []*corev1.Node

func (o NodeByWeight) Len() int      { return len(o) }
func (o NodeByWeight) Swap(i, j int) { o[i], o[j] = o[j], o[i] }
func (o NodeByWeight) Less(i, j int) bool {
	weight1, _ := GetNumberFromAnnotations(o[i].Annotations, annotations.NodeWeight)
	weight2, _ := GetNumberFromAnnotations(o[j].Annotations, annotations.NodeWeight)

	// Fallback to Sorting by name
	if weight1 == weight2 {
		return o[i].Name < o[j].Name
	}

	return weight1 < weight2
}
