// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2016 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package nodeset

import (
	"bytes"
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/kubernetes/pkg/controller/history"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	slinkyv1alpha1 "github.com/SlinkyProject/slurm-operator/api/v1alpha1"
)

type realHistory struct {
	client.Client
}

// NewHistory returns an instance of Interface that uses client to communicate with the API Server and lister to list
// ControllerRevisions. This method should be used to create an Interface for all scenarios other than testing.
func NewHistory(client client.Client) history.Interface {
	return &realHistory{
		Client: client,
	}
}

func (rh *realHistory) ListControllerRevisions(
	parent metav1.Object,
	selector labels.Selector,
) ([]*appsv1.ControllerRevision, error) {
	// List all revisions in the namespace that match the selector
	optsList := &client.ListOptions{
		Namespace:     parent.GetNamespace(),
		LabelSelector: selector,
	}
	revisionList := &appsv1.ControllerRevisionList{}
	err := rh.List(context.TODO(), revisionList, optsList)
	if err != nil {
		return nil, err
	}
	var owned []*appsv1.ControllerRevision
	for i := range revisionList.Items {
		ref := metav1.GetControllerOf(&revisionList.Items[i])
		if ref == nil || ref.UID == parent.GetUID() {
			owned = append(owned, &revisionList.Items[i])
		}
	}
	return owned, err
}

func (rh *realHistory) CreateControllerRevision(
	parent metav1.Object,
	revision *appsv1.ControllerRevision,
	collisionCount *int32,
) (*appsv1.ControllerRevision, error) {
	if collisionCount == nil {
		return nil, fmt.Errorf("collisionCount should not be nil")
	}
	namespace := parent.GetNamespace()

	// Clone the input
	clone := revision.DeepCopy()

	// Continue to attempt to create the revision updating the name with a new hash on each iteration
	for {
		hash := history.HashControllerRevision(revision, collisionCount)
		// Update the revisions name
		clone.Name = history.ControllerRevisionName(parent.GetName(), hash)

		created := clone.DeepCopy()
		created.Namespace = namespace
		err := rh.Create(context.TODO(), created)
		if apierrors.IsAlreadyExists(err) {
			namespacedName := types.NamespacedName{
				Namespace: namespace,
				Name:      clone.Name,
			}
			exists := &appsv1.ControllerRevision{}
			err := rh.Get(context.TODO(), namespacedName, exists)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(exists.Data.Raw, clone.Data.Raw) {
				return exists, nil
			}
			*collisionCount++
			continue
		}
		return created, err
	}
}

func (rh *realHistory) UpdateControllerRevision(
	revision *appsv1.ControllerRevision,
	newRevision int64,
) (*appsv1.ControllerRevision, error) {
	clone := revision.DeepCopy()
	namespacedName := types.NamespacedName{Namespace: clone.Namespace, Name: clone.Name}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if clone.Revision == newRevision {
			return nil
		}
		clone.Revision = newRevision
		updateErr := rh.Update(context.TODO(), clone)
		if updateErr == nil {
			return nil
		}
		got := &appsv1.ControllerRevision{}
		if err := rh.Get(context.TODO(), namespacedName, got); err == nil {
			clone = got
		}
		return updateErr
	})
	return clone, err
}

func (rh *realHistory) DeleteControllerRevision(revision *appsv1.ControllerRevision) error {
	return rh.Delete(context.TODO(), revision)
}

func (rh *realHistory) AdoptControllerRevision(
	parent metav1.Object,
	parentKind schema.GroupVersionKind,
	revision *appsv1.ControllerRevision,
) (*appsv1.ControllerRevision, error) {
	clone := revision.DeepCopy()
	namespacedName := types.NamespacedName{
		Namespace: clone.Namespace,
		Name:      clone.Name,
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Return an error if the parent does not own the revision
		if owner := metav1.GetControllerOf(clone); owner != nil {
			return fmt.Errorf("attempt to adopt revision owned by %v", owner)
		}

		clone.OwnerReferences = append(clone.OwnerReferences, metav1.OwnerReference{
			APIVersion:         parentKind.GroupVersion().String(),
			Kind:               parentKind.Kind,
			Name:               parent.GetName(),
			UID:                parent.GetUID(),
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		})

		updateErr := rh.Update(context.TODO(), clone)
		if updateErr == nil {
			return nil
		}

		got := &appsv1.ControllerRevision{}
		if err := rh.Get(context.TODO(), namespacedName, got); err == nil {
			clone = got
		}
		return updateErr
	})

	if err != nil {
		return nil, err
	}
	return clone, nil
}

func (rh *realHistory) ReleaseControllerRevision(
	parent metav1.Object,
	revision *appsv1.ControllerRevision,
) (*appsv1.ControllerRevision, error) {
	clone := revision.DeepCopy()
	namespacedName := types.NamespacedName{
		Namespace: clone.Namespace,
		Name:      clone.Name,
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Return an error if the parent does not own the revision
		if owner := metav1.GetControllerOf(clone); owner != nil {
			return fmt.Errorf("attempt to adopt revision owned by %v", owner)
		}

		var newOwners []metav1.OwnerReference
		for _, o := range clone.OwnerReferences {
			if o.UID == parent.GetUID() {
				continue
			}
			newOwners = append(newOwners, o)
		}
		clone.OwnerReferences = newOwners
		updateErr := rh.Update(context.TODO(), clone)
		if updateErr == nil {
			return nil
		}

		got := &appsv1.ControllerRevision{}
		if err := rh.Get(context.TODO(), namespacedName, got); err == nil {
			clone = got
		}
		return updateErr
	})

	if err != nil {
		if apierrors.IsNotFound(err) {
			// We ignore deleted revisions.
			return nil, nil
		}
		if apierrors.IsInvalid(err) {
			// We ignore cases where the parent no longer owns the revision or
			// where the revision has no owner.
			return nil, nil
		}
		return nil, err
	}
	return clone, nil
}

// truncateHistory truncates any non-live ControllerRevisions in revisions from set's history. The UpdateRevision and
// CurrentRevision in set's Status are considered to be live. Any revisions associated with the Pods in pods are also
// considered to be live. Non-live revisions are deleted, starting with the revision with the lowest Revision, until
// only RevisionHistoryLimit revisions remain. If the returned error is nil the operation was successful. This method
// expects that revisions is sorted when supplied.
func (nsc *defaultNodeSetControl) truncateHistory(
	ctx context.Context,
	set *slinkyv1alpha1.NodeSet,
	revisions []*appsv1.ControllerRevision,
	current, update *appsv1.ControllerRevision,
) error {
	pods, err := nsc.getNodeSetPods(ctx, set)
	if err != nil {
		return err
	}

	history := make([]*appsv1.ControllerRevision, 0, len(revisions))
	// mark all live revisions
	live := map[string]bool{}
	if current != nil {
		live[current.Name] = true
	}
	if update != nil {
		live[update.Name] = true
	}
	for i := range pods {
		live[getPodRevision(pods[i])] = true
	}
	// collect live revisions and historic revisions
	for i := range revisions {
		if !live[revisions[i].Name] {
			history = append(history, revisions[i])
		}
	}
	historyLen := len(history)
	historyLimit := int(ptr.Deref(set.Spec.RevisionHistoryLimit, 0))
	if historyLen <= historyLimit {
		return nil
	}
	// delete any non-live history to maintain the revision limit.
	history = history[:(historyLen - historyLimit)]
	for i := 0; i < len(history); i++ {
		if err := nsc.controllerHistory.DeleteControllerRevision(history[i]); err != nil {
			return err
		}
	}
	return nil
}

// newRevision creates a new ControllerRevision containing a patch that reapplies the target state of set.
// The Revision of the returned ControllerRevision is set to revision. If the returned error is nil, the returned
// ControllerRevision is valid. StatefulSet revisions are stored as patches that re-apply the current state of set
// to a new StatefulSet using a strategic merge patch to replace the saved state of the new StatefulSet.
func newRevision(set *slinkyv1alpha1.NodeSet, revision int64, collisionCount *int32) (*appsv1.ControllerRevision, error) {
	patch, err := getPatch(set)
	if err != nil {
		return nil, err
	}
	cr, err := history.NewControllerRevision(
		set,
		controllerKind,
		set.Spec.Template.Labels,
		runtime.RawExtension{Raw: patch},
		revision,
		collisionCount)
	if err != nil {
		return nil, err
	}
	if cr.ObjectMeta.Annotations == nil {
		cr.ObjectMeta.Annotations = make(map[string]string)
	}
	for key, value := range set.Annotations {
		cr.ObjectMeta.Annotations[key] = value
	}
	return cr, nil
}

// nextRevision finds the next valid revision number based on revisions. If the length of revisions
// is 0 this is 1. Otherwise, it is 1 greater than the largest revision's Revision. This method
// assumes that revisions has been sorted by Revision.
func nextRevision(revisions []*appsv1.ControllerRevision) int64 {
	count := len(revisions)
	if count <= 0 {
		return 1
	}
	return revisions[count-1].Revision + 1
}

// getNodeSetRevisions returns the current and update ControllerRevisions for set. It also
// returns a collision count that records the number of name collisions set saw when creating
// new ControllerRevisions. This count is incremented on every name collision and is used in
// building the ControllerRevision names for name collision avoidance. This method may create
// a new revision, or modify the Revision of an existing revision if an update to set is detected.
// This method expects that revisions is sorted when supplied.
func (nsc *defaultNodeSetControl) getNodeSetRevisions(
	set *slinkyv1alpha1.NodeSet,
	revisions []*appsv1.ControllerRevision,
) (*appsv1.ControllerRevision, *appsv1.ControllerRevision, int32, error) {
	var currentRevision, updateRevision *appsv1.ControllerRevision

	revisionCount := len(revisions)
	history.SortControllerRevisions(revisions)

	// Use a local copy of set.Status.CollisionCount to avoid modifying set.Status directly.
	var collisionCount int32
	if set.Status.CollisionCount != nil {
		collisionCount = *set.Status.CollisionCount
	}

	// create a new revision from the current set
	updateRevision, err := newRevision(set, nextRevision(revisions), &collisionCount)
	if err != nil {
		return nil, nil, collisionCount, err
	}

	// find any equivalent revisions
	equalRevisions := history.FindEqualRevisions(revisions, updateRevision)
	equalCount := len(equalRevisions)

	if equalCount > 0 {
		if history.EqualRevision(revisions[revisionCount-1], equalRevisions[equalCount-1]) {
			// if the equivalent revision is immediately prior the update revision has not changed
			updateRevision = revisions[revisionCount-1]
		} else {
			// if the equivalent revision is not immediately prior we will roll back by incrementing the
			// Revision of the equivalent revision
			updateRevision, err = nsc.controllerHistory.UpdateControllerRevision(
				equalRevisions[equalCount-1],
				updateRevision.Revision)
			if err != nil {
				return nil, nil, collisionCount, err
			}
		}
	} else {
		// if there is no equivalent revision we create a new one
		updateRevision, err = nsc.controllerHistory.CreateControllerRevision(set, updateRevision, &collisionCount)
		if err != nil {
			return nil, nil, collisionCount, err
		}
	}

	// attempt to find the revision that corresponds to the current revision
	for i := range revisions {
		if revisions[i].Name == set.Status.NodeSetHash {
			currentRevision = revisions[i]
			break
		}
	}

	// if the current revision is nil we initialize the history by setting it to the update revision
	if currentRevision == nil {
		currentRevision = updateRevision
	}

	return currentRevision, updateRevision, collisionCount, nil
}
