// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"

	"github.com/SlinkyProject/slurm-operator/internal/annotations"
)

type PodByCreationTimestampAndPhase []*corev1.Pod

func (o PodByCreationTimestampAndPhase) Len() int      { return len(o) }
func (o PodByCreationTimestampAndPhase) Swap(i, j int) { o[i], o[j] = o[j], o[i] }

func (o PodByCreationTimestampAndPhase) Less(i, j int) bool {
	// Scheduled Pod first
	if len(o[i].Spec.NodeName) != 0 && len(o[j].Spec.NodeName) == 0 {
		return true
	}

	if len(o[i].Spec.NodeName) == 0 && len(o[j].Spec.NodeName) != 0 {
		return false
	}

	if o[i].CreationTimestamp.Equal(&o[j].CreationTimestamp) {
		return o[i].Name < o[j].Name
	}
	return o[i].CreationTimestamp.Before(&o[j].CreationTimestamp)
}

type PodByCost []*corev1.Pod

func (o PodByCost) Len() int      { return len(o) }
func (o PodByCost) Swap(i, j int) { o[i], o[j] = o[j], o[i] }
func (o PodByCost) Less(i, j int) bool {
	cost1, _ := GetNumberFromAnnotations(o[i].Annotations, annotations.PodDeletionCost)
	cost2, _ := GetNumberFromAnnotations(o[j].Annotations, annotations.PodDeletionCost)

	// Fallback to Sorting by CreationTimestamp
	if cost1 == cost2 {
		if o[i].CreationTimestamp.Equal(&o[j].CreationTimestamp) {
			return o[i].Name < o[j].Name
		}
		return o[i].CreationTimestamp.Before(&o[j].CreationTimestamp)
	}

	return cost1 < cost2
}

// isRunningAndReady returns true if pod is in the PodRunning Phase, if it has a condition of PodReady.
func IsRunningAndReady(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning && podutil.IsPodReady(pod)
}

func IsRunningAndAvailable(pod *corev1.Pod, minReadySeconds int32) bool {
	return podutil.IsPodAvailable(pod, minReadySeconds, metav1.Now())
}

// isCreated returns true if pod has been created and is maintained by the API server
func IsCreated(pod *corev1.Pod) bool {
	return pod.Status.Phase != ""
}

// isPending returns true if pod has a Phase of PodPending
func IsPending(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodPending
}

// isFailed returns true if pod has a Phase of PodFailed
func IsFailed(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodFailed
}

// isSucceeded returns true if pod has a Phase of PodSucceeded
func IsSucceeded(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded
}

// isTerminating returns true if pod's DeletionTimestamp has been set
func IsTerminating(pod *corev1.Pod) bool {
	return pod.DeletionTimestamp != nil
}

// isHealthy returns true if pod is running and ready and has not been terminated
func IsHealthy(pod *corev1.Pod) bool {
	return IsRunningAndReady(pod) && !IsTerminating(pod)
}
