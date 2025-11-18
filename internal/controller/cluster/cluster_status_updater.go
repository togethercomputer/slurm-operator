// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	slinkyv1alpha1 "github.com/SlinkyProject/slurm-operator/api/v1alpha1"
)

// ClusterStatusUpdaterInterface is an interface used to update the ClusterStatus associated with a StatefulSet.
// For any use other than testing, clients should create an instance using NewRealClusterStatusUpdater.
type ClusterStatusUpdaterInterface interface {
	// UpdateClusterStatus sets the set's Status to status. Implementations are required to retry on conflicts,
	// but fail on other errors. If the returned error is nil set's Status has been successfully set to status.
	UpdateClusterStatus(ctx context.Context, cluster *slinkyv1alpha1.Cluster, status *slinkyv1alpha1.ClusterStatus) error
}

// NewRealClusterStatusUpdater returns a ClusterStatusUpdaterInterface that updates the Status of a StatefulSet,
// using the supplied client and setLister.
func NewRealClusterStatusUpdater(client client.Client) ClusterStatusUpdaterInterface {
	return &realClusterStatusUpdater{client}
}

type realClusterStatusUpdater struct {
	client.Client
}

func (csu *realClusterStatusUpdater) UpdateClusterStatus(
	ctx context.Context,
	cluster *slinkyv1alpha1.Cluster,
	status *slinkyv1alpha1.ClusterStatus,
) error {
	logger := log.FromContext(ctx)
	if status == nil {
		logger.Info("No Cluster status given, skipping status update.")
		return nil
	}
	// do not wait due to limited number of clients, but backoff after the default number of steps
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		namespacedName := types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      cluster.Name,
		}
		cluster.Status = *status
		updateErr := csu.Status().Update(ctx, cluster)
		if updateErr == nil {
			// Done
			return nil
		}
		// Refresh Cluster for next retry
		updated := slinkyv1alpha1.Cluster{}
		if err := csu.Get(ctx, namespacedName, &updated); err == nil {
			// make a copy so we don't mutate the shared cache
			cluster = updated.DeepCopy()
		} else {
			utilruntime.HandleError(fmt.Errorf("error getting updated Cluster(%s): %v", klog.KObj(cluster), err))
		}
		return updateErr
	})
}

var _ ClusterStatusUpdaterInterface = &realClusterStatusUpdater{}
