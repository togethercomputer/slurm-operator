// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuilder_BuildContainer(t *testing.T) {
	tests := []struct {
		name   string
		client client.Client
		opts   ContainerOpts
		want   corev1.Container
	}{
		{
			name:   "empty",
			client: fake.NewFakeClient(),
			opts:   ContainerOpts{},
			want:   corev1.Container{},
		},
		{
			name:   "merge",
			client: fake.NewFakeClient(),
			opts: ContainerOpts{
				base: corev1.Container{
					Name:            "foo",
					ImagePullPolicy: corev1.PullIfNotPresent,
					Args:            []string{"-a", "-b"},
					Ports: []corev1.ContainerPort{
						{Name: "slurmd", ContainerPort: 6818},
						{Name: "metrics", ContainerPort: 9090},
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("250m"),
							corev1.ResourceMemory: resource.MustParse("500Mi"),
						},
					},
				},
				merge: corev1.Container{
					Name:  "bar",
					Image: "nginx",
					Args:  []string{"-c"},
					Ports: []corev1.ContainerPort{
						{Name: "slurmd", ContainerPort: 6818, HostPort: 6818},
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100m"),
						},
					},
				},
			},
			want: corev1.Container{
				Name:            "bar",
				Image:           "nginx",
				ImagePullPolicy: corev1.PullIfNotPresent,
				Args:            []string{"-a", "-b", "-c"},
				Ports: []corev1.ContainerPort{
					{Name: "slurmd", ContainerPort: 6818, HostPort: 6818},
					{Name: "metrics", ContainerPort: 9090},
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("100m"),
					},
				},
			},
		},
		{
			name:   "override default TCP port with explicit TCP",
			client: fake.NewFakeClient(),
			opts: ContainerOpts{
				base: corev1.Container{
					Ports: []corev1.ContainerPort{
						{Name: "default-tcp", ContainerPort: 6818},
					},
				},
				merge: corev1.Container{
					Ports: []corev1.ContainerPort{
						{Name: "explicit-tcp", ContainerPort: 6818, Protocol: corev1.ProtocolTCP},
					},
				},
			},
			want: corev1.Container{
				Ports: []corev1.ContainerPort{
					{Name: "explicit-tcp", ContainerPort: 6818, Protocol: corev1.ProtocolTCP},
				},
			},
		},
		{
			name:   "keep ports with the same number and different protocols",
			client: fake.NewFakeClient(),
			opts: ContainerOpts{
				base: corev1.Container{
					Ports: []corev1.ContainerPort{
						{Name: "tcp", ContainerPort: 6818},
					},
				},
				merge: corev1.Container{
					Ports: []corev1.ContainerPort{
						{Name: "udp", ContainerPort: 6818, Protocol: corev1.ProtocolUDP},
					},
				},
			},
			want: corev1.Container{
				Ports: []corev1.ContainerPort{
					{Name: "tcp", ContainerPort: 6818},
					{Name: "udp", ContainerPort: 6818, Protocol: corev1.ProtocolUDP},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(tt.client)
			got := b.BuildContainer(tt.opts)
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("Builder.BuildContainer() = %v", got)
				return
			}
		})
	}
}
