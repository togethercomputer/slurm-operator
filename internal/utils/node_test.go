// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	"github.com/SlinkyProject/slurm-operator/internal/annotations"
	corev1 "k8s.io/api/core/v1"
)

func TestNodeByWeight_Len(t *testing.T) {
	tests := []struct {
		name string
		o    NodeByWeight
		want int
	}{
		{
			name: "empty list",
			o:    []*corev1.Node{},
			want: 0,
		},
		{
			name: "single list",
			o:    append([]*corev1.Node{}, &corev1.Node{}),
			want: 1,
		},
		{
			name: "double list",
			o:    append([]*corev1.Node{}, &corev1.Node{}, &corev1.Node{}),
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.o.Len(); got != tt.want {
				t.Errorf("NodeByWeight.Len() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNodeByWeight_Swap(t *testing.T) {
	nodeA := corev1.Node{}
	nodeA.SetAnnotations(map[string]string{
		annotations.NodeWeight: "1",
	})
	nodeB := corev1.Node{}
	nodeB.SetAnnotations(map[string]string{
		annotations.NodeWeight: "99",
	})
	type args struct {
		i int
		j int
	}
	tests := []struct {
		name string
		o    NodeByWeight
		args args
		want int32
	}{
		{
			name: "swap",
			o:    append([]*corev1.Node{}, &nodeA, &nodeB),
			args: args{
				i: 0,
				j: 1,
			},
			want: 99,
		},
		{
			name: "swap",
			o:    append([]*corev1.Node{}, &nodeB, &nodeA),
			args: args{
				i: 1,
				j: 0,
			},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.o.Swap(tt.args.i, tt.args.j)
			weight1, _ := GetNumberFromAnnotations(tt.o[0].Annotations, annotations.NodeWeight)
			if weight1 != tt.want {
				t.Errorf("weight1 = %v, wanted %v", weight1, tt.want)
			}
		})
	}
}

func TestNodeByWeight_Less(t *testing.T) {
	nodeA := corev1.Node{}
	nodeA.SetAnnotations(map[string]string{
		annotations.NodeWeight: "1",
	})
	nodeA.SetName("AAA")
	nodeB := corev1.Node{}
	nodeB.SetAnnotations(map[string]string{
		annotations.NodeWeight: "99",
	})
	nodeB.SetName("BBB")
	nodeC := corev1.Node{}
	nodeC.SetAnnotations(map[string]string{
		annotations.NodeWeight: "99",
	})
	nodeC.SetName("CCC")
	nodes := append([]*corev1.Node{}, &nodeA, &nodeB, &nodeC)
	type args struct {
		i int
		j int
	}
	tests := []struct {
		name string
		o    NodeByWeight
		args args
		want bool
	}{
		{
			name: "Is 1 less than 99",
			o:    nodes,
			args: args{
				i: 0,
				j: 1,
			},
			want: true,
		},
		{
			name: "Is 99 less than 1",
			o:    nodes,
			args: args{
				i: 1,
				j: 0,
			},
			want: false,
		},
		{
			name: "Is 99 less than 99 (by name)",
			o:    nodes,
			args: args{
				i: 1,
				j: 2,
			},
			want: true,
		},
		{
			name: "Is 99 less than 99 (by name)",
			o:    nodes,
			args: args{
				i: 2,
				j: 1,
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.o.Less(tt.args.i, tt.args.j); got != tt.want {
				t.Errorf("NodeByWeight.Less() = %v, want %v", got, tt.want)
			}
		})
	}
}
