// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeset

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	slinkyv1alpha1 "github.com/togethercomputer/slurm-operator/api/v1alpha1"
	"github.com/togethercomputer/slurm-operator/internal/utils"
)

// NodeSetStatusUpdaterInterface is an interface used to update the NodeSetStatus associated with a StatefulSet.
// For any use other than testing, clients should create an instance using NewRealNodeSetStatusUpdater.
type NodeSetStatusUpdaterInterface interface {
	// UpdateNodeSetStatus sets the set's Status to status. Implementations are required to retry on conflicts,
	// but fail on other errors. If the returned error is nil set's Status has been successfully set to status.
	UpdateNodeSetStatus(ctx context.Context, set *slinkyv1alpha1.NodeSet, status *slinkyv1alpha1.NodeSetStatus) error
}

// NewRealNodeSetStatusUpdater returns a NodeSetStatusUpdaterInterface that updates the Status of a StatefulSet,
// using the supplied client and setLister.
func NewRealNodeSetStatusUpdater(client client.Client) NodeSetStatusUpdaterInterface {
	return &realNodeSetStatusUpdater{client}
}

type realNodeSetStatusUpdater struct {
	client.Client
}

func (nsu *realNodeSetStatusUpdater) UpdateNodeSetStatus(
	ctx context.Context,
	set *slinkyv1alpha1.NodeSet,
	status *slinkyv1alpha1.NodeSetStatus,
) error {
	logger := log.FromContext(ctx)
	if status == nil {
		logger.Info("No NodeSet status given, skipping status update.")
		return nil
	}
	// do not wait due to limited number of clients, but backoff after the default number of steps
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		namespacedName := types.NamespacedName{
			Namespace: set.Namespace,
			Name:      set.Name,
		}
		set.Status = *status
		updateErr := nsu.Status().Update(ctx, set)
		if updateErr == nil {
			// Done
			return nil
		}
		// Refresh NodeSet for next retry
		updated := slinkyv1alpha1.NodeSet{}
		if err := nsu.Get(ctx, namespacedName, &updated); err == nil {
			// make a copy so we don't mutate the shared cache
			set = updated.DeepCopy()
		} else {
			utilruntime.HandleError(fmt.Errorf("error getting updated NodeSet(%s): %v", utils.KeyFunc(set), err))
		}
		return updateErr
	})
}

var _ NodeSetStatusUpdaterInterface = &realNodeSetStatusUpdater{}
