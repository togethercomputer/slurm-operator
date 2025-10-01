// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeset

import (
	"context"
	"flag"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	slinkyv1alpha1 "github.com/togethercomputer/slurm-operator/api/v1alpha1"
	"github.com/togethercomputer/slurm-operator/internal/resources"
	"github.com/togethercomputer/slurm-operator/internal/utils/durationstore"
)

const (
	// BurstReplicas is a rate limiter for booting pods on a lot of pods.
	// The value of 250 is chosen b/c values that are too high can cause registry DoS issues.
	BurstReplicas = 250

	// BackoffGCInterval is the time that has to pass before next iteration of backoff GC is run
	BackoffGCInterval = 1 * time.Minute
)

// Reasons for NodeSet events
const (
	// SelectingAllReason is added to an event when a NodeSet selects all Pods.
	SelectingAllReason = "SelectingAll"
	// FailedPlacementReason is added to an event when a NodeSet cannot schedule a Pod to a specified node.
	FailedPlacementReason = "FailedPlacement"
	// FailedNodeSetPodReason is added to an event when the status of a Pod of a NodeSet is 'Failed'.
	FailedNodeSetPodReason = "FailedNodeSetPod"
)

func init() {
	flag.IntVar(&maxConcurrentReconciles, "nodeset-workers", maxConcurrentReconciles, "Max concurrent workers for NodeSet controller.")
}

var (
	// controllerKind contains the schema.GroupVersionKind for this controller type.
	controllerKind = slinkyv1alpha1.SchemeGroupVersion.WithKind("NodeSet")

	maxConcurrentReconciles = 1

	// this is a short cut for any sub-functions to notify the reconcile how long to wait to requeue
	durationStore = durationstore.NewDurationStore(durationstore.Greater)

	onceBackoffGC     sync.Once
	failedPodsBackoff = flowcontrol.NewBackOff(1*time.Second, 15*time.Minute)
)

// NodeSetReconciler reconciles a NodeSet object
type NodeSetReconciler struct {
	client.Client
	KubeClient *kubernetes.Clientset
	Scheme     *runtime.Scheme

	SlurmClusters *resources.Clusters
	EventCh       chan event.GenericEvent

	control NodeSetControlInterface
}

//+kubebuilder:rbac:groups=slinky.slurm.net,resources=nodesets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=slinky.slurm.net,resources=nodesets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=slinky.slurm.net,resources=nodesets/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *NodeSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, retErr error) {
	logger := log.FromContext(ctx)

	logger.Info("Started syncing NodeSet", "request", req)

	onceBackoffGC.Do(func() {
		go wait.Until(failedPodsBackoff.GC, BackoffGCInterval, ctx.Done())
	})

	startTime := time.Now()
	defer func() {
		if retErr == nil {
			if res.Requeue || res.RequeueAfter > 0 {
				logger.Info("Finished syncing NodeSet", "duration", time.Since(startTime), "result", res)
			} else {
				logger.Info("Finished syncing NodeSet", "duration", time.Since(startTime))
			}
		} else {
			logger.Info("Finished syncing NodeSet", "duration", time.Since(startTime), "error", retErr)
		}
		// clean the duration store
		_ = durationStore.Pop(req.String())
	}()

	retErr = r.control.SyncNodeSet(ctx, req)
	res = reconcile.Result{
		RequeueAfter: durationStore.Pop(req.String()),
	}
	return res, retErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	eventRecorder := record.NewBroadcaster().NewRecorder(r.Scheme, corev1.EventSource{Component: "nodeset-controller"})
	r.control = NewDefaultNodeSetControl(
		r.Client,
		r.KubeClient,
		eventRecorder,
		NewNodeSetPodControl(r.Client, eventRecorder, r.SlurmClusters),
		NewRealNodeSetStatusUpdater(r.Client),
		NewHistory(r.Client),
		r.SlurmClusters,
	)
	podEventHandler := podEventHandler{
		Reader: mgr.GetCache(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("nodeset-controller").
		For(&slinkyv1alpha1.NodeSet{}).
		Owns(&corev1.Pod{}).
		Watches(&corev1.Node{}, &nodeEventHandler{
			reader: mgr.GetCache(),
		}).
		Watches(&corev1.Pod{}, &podEventHandler).
		WatchesRawSource(
			source.Channel(
				r.EventCh,
				&podEventHandler,
			),
		).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}
