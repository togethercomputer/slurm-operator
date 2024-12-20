// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2017 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package nodeset

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/pkg/controller/daemon/util"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v0041 "github.com/SlinkyProject/slurm-client/api/v0041"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	slinkyv1alpha1 "github.com/SlinkyProject/slurm-operator/api/v1alpha1"
	"github.com/SlinkyProject/slurm-operator/internal/annotations"
	"github.com/SlinkyProject/slurm-operator/internal/utils"
	"github.com/SlinkyProject/slurm-operator/internal/utils/podinfo"
)

// updatedDesiredNodeCounts calculates the true number of allowed unavailable or surge pods and
// updates the nodeToNodeSetPods array to include an empty array for every node that is not scheduled.
func (nsc *defaultNodeSetControl) updatedDesiredNodeCounts(
	logger klog.Logger,
	nodeset *slinkyv1alpha1.NodeSet,
	nodes []*corev1.Node,
	nodeToNodeSetPods map[*corev1.Node][]*corev1.Pod,
) (int, int, error) {
	var desiredNumberScheduled int
	for i := range nodes {
		node := nodes[i]
		wantToRun, _ := nodeShouldRunNodeSetPod(node, nodeset)
		if !wantToRun {
			continue
		}
		desiredNumberScheduled++

		if _, exists := nodeToNodeSetPods[node]; !exists {
			nodeToNodeSetPods[node] = nil
		}
	}

	if nodeset.Spec.Replicas != nil {
		desiredNumberScheduled = int(ptr.Deref(nodeset.Spec.Replicas, 0))
	}

	maxUnavailable, err := unavailableCount(nodeset, desiredNumberScheduled)
	if err != nil {
		return -1, -1, fmt.Errorf("invalid value for MaxUnavailable: %v", err)
	}

	// if the daemonset returned with an impossible configuration, obey the default of unavailable=1 (in the
	// event the apiserver returns 0 for both surge and unavailability)
	if desiredNumberScheduled > 0 && maxUnavailable == 0 {
		logger.Info("NodeSet is not configured for unavailability, defaulting to accepting unavailability",
			"NodeSet", klog.KObj(nodeset))
		maxUnavailable = 1
	}
	return maxUnavailable, desiredNumberScheduled, nil
}

func GetTemplateGeneration(nodeset *slinkyv1alpha1.NodeSet) (*int64, error) {
	annotation, found := nodeset.Annotations[appsv1.DeprecatedTemplateGeneration]
	if !found {
		return nil, nil
	}
	generation, err := strconv.ParseInt(annotation, 10, 64)
	if err != nil {
		return nil, err
	}
	return &generation, nil
}

func (nsc *defaultNodeSetControl) filterNodeSetPodsToUpdate(
	nodeset *slinkyv1alpha1.NodeSet,
	nodes []*corev1.Node,
	hash string,
	nodeToNodeSetPods map[*corev1.Node][]*corev1.Pod,
) (map[*corev1.Node][]*corev1.Pod, error) {
	existingNodes := sets.NewString()
	for _, node := range nodes {
		existingNodes.Insert(node.Name)
	}
	for node := range nodeToNodeSetPods {
		if !existingNodes.Has(node.Name) {
			delete(nodeToNodeSetPods, node)
		}
	}

	nodeNames, err := nsc.filterNodeSetPodsNodeToUpdate(nodeset, hash, nodeToNodeSetPods)
	if err != nil {
		return nil, err
	}

	ret := make(map[*corev1.Node][]*corev1.Pod, len(nodeNames))
	for _, name := range nodeNames {
		ret[name] = nodeToNodeSetPods[name]
	}
	return ret, nil
}

func (nsc *defaultNodeSetControl) filterNodeSetPodsNodeToUpdate(
	nodeset *slinkyv1alpha1.NodeSet,
	hash string,
	nodeToNodeSetPods map[*corev1.Node][]*corev1.Pod,
) ([]*corev1.Node, error) {
	var partition int32
	if nodeset.Spec.UpdateStrategy.RollingUpdate != nil {
		partition = ptr.Deref(nodeset.Spec.UpdateStrategy.RollingUpdate.Partition, 0)
	}
	var allNodes []*corev1.Node
	for node := range nodeToNodeSetPods {
		allNodes = append(allNodes, node)
	}
	sort.Sort(utils.NodeByWeight(allNodes))

	var updated []*corev1.Node
	var updating []*corev1.Node
	var rest []*corev1.Node
	for node := range nodeToNodeSetPods {
		newPod, oldPod, ok := findUpdatedPodsOnNode(nodeset, nodeToNodeSetPods[node], hash)
		if !ok || newPod != nil || oldPod != nil {
			updated = append(updated, node)
			continue
		}
		rest = append(rest, node)
	}

	sorted := append(updated, updating...)
	sorted = append(sorted, rest...)
	if maxUpdate := len(allNodes) - int(partition); maxUpdate <= 0 {
		return nil, nil
	} else if maxUpdate < len(sorted) {
		sorted = sorted[:maxUpdate]
	}
	return sorted, nil
}

// syncNodeSetRollingUpdate identifies the nodeset of old pods to in-place update, delete, or additional pods to create on nodes,
// remaining within the constraints imposed by the update strategy.
func (nsc *defaultNodeSetControl) syncNodeSetRollingUpdate(
	ctx context.Context,
	nodeset *slinkyv1alpha1.NodeSet,
	nodes []*corev1.Node,
	nodeToNodeSetPods map[*corev1.Node][]*corev1.Pod,
	hash string,
) error {
	logger := log.FromContext(ctx)

	maxUnavailable, _, err := nsc.updatedDesiredNodeCounts(logger, nodeset, nodes, nodeToNodeSetPods)
	if err != nil {
		return fmt.Errorf("could not get unavailable numbers: %v", err)
	}

	// Advanced: filter the pods updated, updating and can update, according to partition and selector
	nodeToNodeSetPods, err = nsc.filterNodeSetPodsToUpdate(nodeset, nodes, hash, nodeToNodeSetPods)
	if err != nil {
		return fmt.Errorf("failed to filterNodeSetPodsToUpdate: %v", err)
	}

	now := failedPodsBackoff.Clock.Now()

	// We delete just enough pods to stay under the maxUnavailable limit, if any
	// are necessary, and let syncNodeSet create new instances on those nodes.
	//
	// Assumptions:
	// * Expect syncNodeSet to allow no more than one pod per node
	// * Expect syncNodeSet will create new pods
	// * Expect syncNodeSet will handle failed pods
	// * Deleted pods do not count as unavailable so that updates make progress when nodes are down
	// Invariants:
	// * The number of new pods that are unavailable must be less than maxUnavailable
	// * A node with an available old pod is a candidate for deletion if it does not violate other invariants
	//
	var numUnavailable int
	var allowedReplacementPods []*corev1.Pod
	var candidatePodsToDelete []*corev1.Pod
	for node, pods := range nodeToNodeSetPods {
		newPod, oldPod, ok := findUpdatedPodsOnNode(nodeset, pods, hash)
		if !ok {
			// let the syncNodeSet clean up this node, and treat it as an unavailable node
			logger.V(1).Info("NodeSet has excess pods on Node, skipping to allow the core loop to process",
				"NodeSet", klog.KObj(nodeset), "Node", klog.KObj(node))
			numUnavailable++
			continue
		}
		switch {
		case oldPod == nil && newPod == nil, oldPod != nil && newPod != nil:
			// syncNodeSet will handle creating or deleting the appropriate pod
		case newPod != nil:
			// this pod is up to date, check its availability
			if !podutil.IsPodAvailable(newPod, nodeset.Spec.MinReadySeconds, metav1.Time{Time: now}) {
				// an unavailable new pod is counted against maxUnavailable
				numUnavailable++
				logger.V(1).Info("NodeSet Pod on Node is new and unavailable",
					"NodeSet", klog.KObj(nodeset), "Pod", klog.KObj(newPod), "Node", klog.KObj(node))
			}
		default:
			// this pod is old, it is an update candidate
			switch {
			case !podutil.IsPodAvailable(oldPod, nodeset.Spec.MinReadySeconds, metav1.Time{Time: now}), isNodeSetPodDelete(oldPod):
				// the old pod is not available, so it needs to be replaced
				logger.V(1).Info("NodeSet Pod on Node is out of date and not available, allowing replacement",
					"NodeSet", klog.KObj(nodeset), "Pod", klog.KObj(oldPod), "Node", klog.KObj(node))
				// record the replacement
				if allowedReplacementPods == nil {
					allowedReplacementPods = make([]*corev1.Pod, 0, len(nodeToNodeSetPods))
				}
				allowedReplacementPods = append(allowedReplacementPods, oldPod)
			case numUnavailable >= maxUnavailable:
				// no point considering any other candidates
				continue
			default:
				logger.V(1).Info("NodeSet Pod on Node is out of date, this is a candidate to replace",
					"NodeSet", klog.KObj(nodeset), "Pod", klog.KObj(oldPod), "Node", klog.KObj(node))
				// record the candidate
				if candidatePodsToDelete == nil {
					candidatePodsToDelete = make([]*corev1.Pod, 0, maxUnavailable)
				}
				candidatePodsToDelete = append(candidatePodsToDelete, oldPod)
			}
		}
	}

	// use any of the candidates we can, including the allowedReplacementPods
	logger.V(1).Info("NodeSet allowing replacements",
		"NodeSet", klog.KObj(nodeset),
		"allowedReplacementPods", len(allowedReplacementPods),
		"maxUnavailable", maxUnavailable,
		"numUnavailable", numUnavailable,
		"candidatePodsToDelete", len(candidatePodsToDelete))
	remainingUnavailable := maxUnavailable - numUnavailable
	if remainingUnavailable < 0 {
		remainingUnavailable = 0
	}
	if max := len(candidatePodsToDelete); remainingUnavailable > max {
		remainingUnavailable = max
	}
	oldPodsToDelete := append(allowedReplacementPods, candidatePodsToDelete[:remainingUnavailable]...)

	return nsc.syncNodeSetPods(ctx, nodeset, oldPodsToDelete, nil)
}

// updateSlurmNodeWithPodInfo updated the corresponding Slurm node with info of
// the Pod that backs it.
func (nsc *defaultNodeSetControl) updateSlurmNodeWithPodInfo(
	ctx context.Context,
	nodeset *slinkyv1alpha1.NodeSet,
	pod *corev1.Pod,
) error {
	logger := log.FromContext(ctx)

	namespacedName := types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Name,
	}
	freshPod := &corev1.Pod{}
	if err := nsc.Get(ctx, namespacedName, freshPod); err != nil {
		return err
	}

	clusterName := types.NamespacedName{
		Namespace: nodeset.GetNamespace(),
		Name:      nodeset.Spec.ClusterName,
	}
	slurmClient := nsc.slurmClusters.Get(clusterName)
	if slurmClient != nil && !isNodeSetPodDelete(pod) {
		objectKey := object.ObjectKey(pod.Spec.Hostname)
		slurmNode := &slurmtypes.V0041Node{}
		if err := slurmClient.Get(ctx, objectKey, slurmNode); err != nil {
			return err
		}

		oldPodInfo := podinfo.PodInfo{}
		_ = podinfo.ParseIntoPodInfo(slurmNode.Comment, &oldPodInfo)
		podInfo := podinfo.PodInfo{
			Namespace: pod.Namespace,
			PodName:   pod.Name,
		}

		if oldPodInfo.Equal(podInfo) {
			// Avoid needless update request
			return nil
		}

		logger.Info("Update Slurm Node with Kubernetes Pod info",
			"Node", slurmNode.Name, "PodInfo", podInfo)

		req := v0041.V0041UpdateNodeMsg{
			Comment: ptr.To(podInfo.ToString()),
		}
		if err := slurmClient.Update(ctx, slurmNode, req); err != nil {
			return err
		}
	}

	return nil
}

func tolerateError(err error) bool {
	if err == nil {
		return true
	}
	errText := err.Error()
	if errText == http.StatusText(http.StatusNotFound) ||
		errText == http.StatusText(http.StatusNoContent) {
		return true
	}
	return false
}

// syncSlurm processes Slurm Nodes to align them with Kubernetes, and vice versa.
func (nsc *defaultNodeSetControl) syncSlurm(
	ctx context.Context,
	nodeset *slinkyv1alpha1.NodeSet,
	nodes []*corev1.Node,
	nodeToNodeSetPods map[*corev1.Node][]*corev1.Pod,
) error {
	logger := log.FromContext(ctx)

	clusterName := types.NamespacedName{
		Namespace: nodeset.GetNamespace(),
		Name:      nodeset.Spec.ClusterName,
	}
	slurmClient := nsc.slurmClusters.Get(clusterName)
	if slurmClient == nil {
		return nil
	}

	nodeList := &slurmtypes.V0041NodeList{}
	if err := slurmClient.List(ctx, nodeList); !tolerateError(err) {
		return err
	}

	kubeNodes := sets.NewString()
	for _, node := range nodes {
		nodeSetPods, exists := nodeToNodeSetPods[node]
		if !exists {
			continue
		}
		kubeNodes.Insert(node.Name)
		for _, pod := range nodeSetPods {
			if !utils.IsHealthy(pod) {
				continue
			}
			if err := nsc.updateSlurmNodeWithPodInfo(ctx, nodeset, pod); !tolerateError(err) {
				return err
			}
		}
	}

	slurmNodes := sets.NewString()
	for _, node := range nodeList.Items {
		hasCommunicationFailure := node.GetStateAsSet().HasAll(v0041.V0041NodeStateDOWN, v0041.V0041NodeStateNOTRESPONDING)
		podInfo := podinfo.PodInfo{}
		_ = podinfo.ParseIntoPodInfo(node.Comment, &podInfo)
		noPodInfo := podInfo.Equal(podinfo.PodInfo{})
		if kubeNodes.Has(*node.Name) || !hasCommunicationFailure || noPodInfo {
			slurmNodes.Insert(*node.Name)
			continue
		}
		logger.Info("Deleting Slurm Node without a corresponding Pod", "Node", node.Name, "Pod", node.Comment)
		if err := slurmClient.Delete(ctx, &node); !tolerateError(err) {
			return err
		}
	}

	for _, node := range nodes {
		nodeSetPods, exists := nodeToNodeSetPods[node]
		if !exists {
			continue
		}
		for _, pod := range nodeSetPods {
			if slurmNodes.Has(pod.Spec.Hostname) || !utils.IsHealthy(pod) || !utils.IsRunningAndAvailable(pod, 30) || isNodeSetPodDelete(pod) {
				continue
			}
			toUpdate := pod.DeepCopy()
			toUpdate.Annotations[annotations.PodDelete] = "true"
			if err := nsc.Update(ctx, toUpdate); err != nil {
				if apierrors.IsNotFound(err) {
					return err
				}
			}
		}
	}

	return nil
}

// inconsistentStatus returns true if the ObservedGeneration of status is greater than nodeset's
// Generation or if any of the status's fields do not match those of nodeset's status.
func inconsistentStatus(nodeset *slinkyv1alpha1.NodeSet, status *slinkyv1alpha1.NodeSetStatus) bool {
	return status.ObservedGeneration > nodeset.Status.ObservedGeneration ||
		status.DesiredNumberScheduled != nodeset.Status.DesiredNumberScheduled ||
		status.CurrentNumberScheduled != nodeset.Status.CurrentNumberScheduled ||
		status.NumberMisscheduled != nodeset.Status.NumberMisscheduled ||
		status.NumberReady != nodeset.Status.NumberReady ||
		status.UpdatedNumberScheduled != nodeset.Status.UpdatedNumberScheduled ||
		status.NumberAvailable != nodeset.Status.NumberAvailable ||
		status.NumberUnavailable != nodeset.Status.NumberUnavailable ||
		status.NumberIdle != nodeset.Status.NumberIdle ||
		status.NumberAllocated != nodeset.Status.NumberAllocated ||
		status.NumberDrain != nodeset.Status.NumberDrain ||
		status.NodeSetHash != nodeset.Status.NodeSetHash
}

func (nsc *defaultNodeSetControl) updateStatus(
	ctx context.Context,
	nodeset *slinkyv1alpha1.NodeSet,
	status *slinkyv1alpha1.NodeSetStatus,
) error {
	logger := log.FromContext(ctx)

	// do not perform an update when the status is consistant
	if !inconsistentStatus(nodeset, status) {
		return nil
	}

	logger.V(1).Info("NodeSet status update", "NodeSetStatus", status)

	// copy nodeset and update its status
	nodeset = nodeset.DeepCopy()
	if err := nsc.statusUpdater.UpdateNodeSetStatus(ctx, nodeset, status); err != nil {
		return err
	}

	return nil
}

func (nsc *defaultNodeSetControl) syncNodeSetStatus(
	ctx context.Context,
	nodeset *slinkyv1alpha1.NodeSet,
	nodes []*corev1.Node,
	nodeToNodeSetPods map[*corev1.Node][]*corev1.Pod,
	collisionCount int32,
	hash string,
	updateObservedGen bool,
) error {
	setKey := utils.KeyFunc(nodeset)
	status := nodeset.Status.DeepCopy()

	clusterName := types.NamespacedName{
		Namespace: nodeset.GetNamespace(),
		Name:      nodeset.Spec.ClusterName,
	}
	slurmClient := nsc.slurmClusters.Get(clusterName)

	selector, err := metav1.LabelSelectorAsSelector(nodeset.Spec.Selector)
	if err != nil {
		return fmt.Errorf("could not get label selector for NodeSet(%s): %v", klog.KObj(nodeset), err)
	}

	var numberIdle, numberAllocated, numberDown, numberDrain int32
	var desiredNumberScheduled, currentNumberScheduled, numberMisscheduled, numberReady, updatedNumberScheduled, numberAvailable int32
	now := failedPodsBackoff.Clock.Now()
	for _, node := range nodes {
		shouldRun, _ := nodeShouldRunNodeSetPod(node, nodeset)
		scheduled := len(nodeToNodeSetPods[node]) > 0

		if shouldRun {
			desiredNumberScheduled++
			if scheduled {
				currentNumberScheduled++
				// Sort the nodeset pods by creation time, so that the oldest is first.
				nodeSetPods := nodeToNodeSetPods[node]
				sort.Sort(utils.PodByCreationTimestampAndPhase(nodeSetPods))
				pod := nodeSetPods[0]
				if podutil.IsPodReady(pod) {
					numberReady++
					if isNodeSetPodAvailable(pod, nodeset.Spec.MinReadySeconds, metav1.Time{Time: now}) {
						numberAvailable++
					}
				}
				// If the returned error is not nil we have a parse error.
				// The controller handles this via the hash.
				generation, err := GetTemplateGeneration(nodeset)
				if err != nil {
					generation = nil
				}
				if util.IsPodUpdated(pod, hash, generation) {
					updatedNumberScheduled++
				}
			}
		} else {
			if scheduled {
				numberMisscheduled++
			}
		}

		if slurmClient != nil {
			slurmNode := &slurmtypes.V0041Node{}
			key := object.ObjectKey(node.Name)
			if err := slurmClient.Get(ctx, key, slurmNode); err != nil {
				if err.Error() != http.StatusText(http.StatusNotFound) {
					return fmt.Errorf("failed to get Slurm Node: %v", err)
				}
			}

			podInfo := podinfo.PodInfo{}
			_ = podinfo.ParseIntoPodInfo(slurmNode.Comment, &podInfo)

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: podInfo.Namespace,
					Name:      podInfo.PodName,
				},
			}
			if !isPodFromNodeSet(nodeset, pod) {
				continue
			}

			if utils.IsHealthy(pod) {
				if err := nsc.updateSlurmNodeWithPodInfo(ctx, nodeset, pod); err != nil {
					if err.Error() != http.StatusText(http.StatusNotFound) {
						return err
					}
				}
			}

			// Base Slurm Node States
			switch {
			case slurmNode.GetStateAsSet().Has(v0041.V0041NodeStateIDLE):
				numberIdle++
			case slurmNode.GetStateAsSet().HasAny(v0041.V0041NodeStateALLOCATED, v0041.V0041NodeStateMIXED):
				numberAllocated++
			case slurmNode.GetStateAsSet().Has(v0041.V0041NodeStateDOWN):
				numberDown++
			}
			// Flag Slurm Node State
			if slurmNode.GetStateAsSet().Has(v0041.V0041NodeStateDRAIN) {
				numberDrain++
			}
		}
	}
	if nodeset.Spec.Replicas != nil {
		desiredNumberScheduled = ptr.Deref(nodeset.Spec.Replicas, 0)
	}
	numberUnavailable := desiredNumberScheduled - numberAvailable

	if updateObservedGen {
		status.ObservedGeneration = nodeset.Generation
	}
	status.DesiredNumberScheduled = desiredNumberScheduled
	status.CurrentNumberScheduled = currentNumberScheduled
	status.NumberMisscheduled = numberMisscheduled
	status.NumberReady = numberReady
	status.UpdatedNumberScheduled = updatedNumberScheduled
	status.NumberAvailable = numberAvailable
	status.NumberUnavailable = numberUnavailable
	status.NumberIdle = numberIdle
	status.NumberAllocated = numberAllocated
	status.NumberDown = numberDown
	status.NumberDrain = numberDrain
	status.NodeSetHash = hash
	status.CollisionCount = &collisionCount
	status.Selector = selector.String()

	if err := nsc.updateStatus(ctx, nodeset, status); err != nil {
		return fmt.Errorf("error updating NodeSet(%s) status: %v", setKey, err)
	}

	if nodeset.Spec.MinReadySeconds >= 0 && numberReady != numberAvailable {
		// Resync the NodeSet after MinReadySeconds as a last line of defense to guard against clock-skew.
		durationStore.Push(setKey, time.Duration(nodeset.Spec.MinReadySeconds)*time.Second)
	} else if (numberIdle + numberAllocated) != desiredNumberScheduled {
		// Resync the NodeSet until the Slurm state is correct
		durationStore.Push(setKey, 5*time.Second)
	}

	return nil
}
