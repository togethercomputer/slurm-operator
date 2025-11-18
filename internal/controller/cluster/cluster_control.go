// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/object"
	slurmtypes "github.com/SlinkyProject/slurm-client/pkg/types"

	slinkyv1alpha1 "github.com/SlinkyProject/slurm-operator/api/v1alpha1"
	nodesetcontroller "github.com/SlinkyProject/slurm-operator/internal/controller/nodeset"
	"github.com/SlinkyProject/slurm-operator/internal/resources"
	"github.com/SlinkyProject/slurm-operator/internal/utils"
)

// ClusterControl implements the control logic for synchronizing Clusters and their children Pods. It is implemented
// as an interface to allow for extensions that provide different semantics. Currently, there is only one implementation.
type ClusterControlInterface interface {
	// SyncCluster implements the control logic for managing Slurm cluster connection and data.
	// If an implementation returns a non-nil error, the invocation will be retried using a rate-limited strategy.
	// Implementors should sink any errors that they do not wish to trigger a retry, and they may feel free to
	// exit exceptionally at any point provided they wish the update to be re-run at a later point in time.
	SyncCluster(ctx context.Context, req reconcile.Request) error
}

// NewDefaultClusterControl returns a new instance of the default implementation ClusterControlInterface that
// implements the documented semantics for Clusters.
func NewDefaultClusterControl(
	client client.Client,
	eventRecorder record.EventRecorder,
	statusUpdater ClusterStatusUpdaterInterface,
	slurmClusters *resources.Clusters,
	eventCh chan event.GenericEvent,
) ClusterControlInterface {
	return &defaultClusterControl{
		Client:        client,
		eventRecorder: eventRecorder,
		statusUpdater: statusUpdater,
		slurmClusters: slurmClusters,
		eventCh:       eventCh,
	}
}

type defaultClusterControl struct {
	client.Client
	KubeClient    *kubernetes.Clientset
	eventRecorder record.EventRecorder
	statusUpdater ClusterStatusUpdaterInterface
	slurmClusters *resources.Clusters
	eventCh       chan event.GenericEvent
}

// slurmClientDelete handles stopping and deleing the cluster/slurmClient data.
func (cc *defaultClusterControl) slurmClientDelete(
	ctx context.Context,
	clusterName types.NamespacedName,
) {
	logger := log.FromContext(ctx)

	// Remove slurm client
	if cc.slurmClusters.Remove(clusterName) {
		logger.V(1).Info("Removed slurm cluster client", "clusterName", clusterName.String())
	}
}

// slurmClientUpdate handles updates to the cluster/slurmClient data.
func (cc *defaultClusterControl) slurmClientUpdate(
	ctx context.Context,
	cluster *slinkyv1alpha1.Cluster,
) error {
	logger := log.FromContext(ctx)
	clusterName := types.NamespacedName{
		Namespace: cluster.GetNamespace(),
		Name:      cluster.GetName(),
	}

	// Lookup Secret from Cluster reference
	secret := &corev1.Secret{}
	secretName := cluster.Spec.Token.SecretRef
	secretNamespacedName := types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      secretName,
	}
	if err := cc.Get(ctx, secretNamespacedName, secret); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Secret not found, retry later", "secretName", secretName)
			durationStore.Push(utils.KeyFunc(cluster), requeueSecretTime)
			cc.slurmClientDelete(ctx, clusterName)
			return nil
		}
		logger.Error(err, "Failed to get secret", "secretName", secretName)
		return err
	}
	authToken := string(secret.Data["auth-token"])

	// Lookup slurm client
	server := cluster.Spec.Server
	slurmClientOld := cc.slurmClusters.Get(clusterName)

	// Determine if client is unchanged
	if (slurmClientOld != nil) &&
		(slurmClientOld.GetServer() == server) &&
		(slurmClientOld.GetToken() == authToken) {
		return nil
	}

	// Create slurm client
	config := &slurmclient.Config{
		Server:    server,
		AuthToken: authToken,
	}
	options := &slurmclient.ClientOptions{
		DisableFor: []object.Object{
			&slurmtypes.Ping{},
		},
	}
	slurmClient, err := slurmclient.NewClient(config, options)
	if err != nil {
		logger.Error(err, "Failed to create slurm client")
		return nil
	}
	nodesetcontroller.SetEventHandler(slurmClient, cc.eventCh)

	// Add slurm client
	if cc.slurmClusters.Add(clusterName, slurmClient) {
		logger.Info("Added slurm cluster client", "clusterName", clusterName.String())
	}

	return nil
}

// syncCluster performs the main syncing logic.
func (cc *defaultClusterControl) syncCluster(
	ctx context.Context,
	cluster *slinkyv1alpha1.Cluster,
) error {
	clusterName := types.NamespacedName{
		Namespace: cluster.GetNamespace(),
		Name:      cluster.GetName(),
	}

	// Handle resources marked for deletion
	if cluster.GetDeletionTimestamp() != nil {
		cc.slurmClientDelete(ctx, clusterName)
		return nil
	}

	if err := cc.slurmClientUpdate(ctx, cluster); err != nil {
		return err
	}

	return nil
}

func inconsistentStatus(
	cluster *slinkyv1alpha1.Cluster,
	status *slinkyv1alpha1.ClusterStatus,
) bool {
	return status.IsReady != cluster.Status.IsReady
}

func (cc *defaultClusterControl) updateStatus(
	ctx context.Context,
	cluster *slinkyv1alpha1.Cluster,
	status *slinkyv1alpha1.ClusterStatus,
) error {
	logger := log.FromContext(ctx)

	// do not perform an update when the status is consistant
	if !inconsistentStatus(cluster, status) {
		return nil
	}

	logger.V(1).Info("Cluster status update", "ClusterStatus", status)

	// copy cluster and update its status
	cluster = cluster.DeepCopy()
	if err := cc.statusUpdater.UpdateClusterStatus(ctx, cluster, status); err != nil {
		return err
	}

	return nil
}

// syncClusterStatus handles determining and updating the cluster status.
func (cc *defaultClusterControl) syncClusterStatus(
	ctx context.Context,
	cluster *slinkyv1alpha1.Cluster,
) error {
	logger := log.FromContext(ctx)
	status := cluster.Status.DeepCopy()

	clusterName := types.NamespacedName{
		Namespace: cluster.GetNamespace(),
		Name:      cluster.GetName(),
	}
	isReady := false

	if slurmClient := cc.slurmClusters.Get(clusterName); slurmClient != nil {
		pingList := &slurmtypes.PingList{}
		err := slurmClient.List(ctx, pingList)
		if err != nil {
			logger.Error(err, "unable to ping cluster")
		} else {
			for _, ping := range pingList.Items {
				if ping.Pinged {
					isReady = true
					break
				}
			}
		}
	}

	status.IsReady = isReady

	if err := cc.updateStatus(ctx, cluster, status); err != nil {
		return fmt.Errorf("error updating Cluster(%s) status: %v", clusterName, err)
	}

	if !status.IsReady {
		durationStore.Push(utils.KeyFunc(cluster), requeueReadyTime)
	}

	return nil
}

// SyncCluster implements ClusterControlInterface.
func (cc *defaultClusterControl) SyncCluster(ctx context.Context, req reconcile.Request) error {
	logger := log.FromContext(ctx)
	clusterName := req.NamespacedName

	cluster := &slinkyv1alpha1.Cluster{}
	if err := cc.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Cluster has been deleted.", "request", req)
			cc.slurmClientDelete(ctx, clusterName)
			return nil
		}
		return err
	}

	// Make a copy now to avoid mutation errors.
	cluster = cluster.DeepCopy()

	if err := cc.syncCluster(ctx, cluster); err != nil {
		errors := []error{err}
		if err := cc.syncClusterStatus(ctx, cluster); err != nil {
			errors = append(errors, err)
		}
		return utilerrors.NewAggregate(errors)
	}

	return cc.syncClusterStatus(ctx, cluster)
}
