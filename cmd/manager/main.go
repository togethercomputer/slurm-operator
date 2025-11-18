// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	slinkyv1alpha1 "github.com/SlinkyProject/slurm-operator/api/v1alpha1"
	"github.com/SlinkyProject/slurm-operator/internal/controller/cluster"
	"github.com/SlinkyProject/slurm-operator/internal/controller/nodeset"
	"github.com/SlinkyProject/slurm-operator/internal/resources"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(slinkyv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

// Input flags to the command
type Flags struct {
	enableLeaderElection bool
	probeAddr            string
}

func parseFlags(flags *Flags) {
	flag.StringVar(
		&flags.probeAddr,
		"health-probe-bind-address",
		":8081",
		"The address the probe endpoint binds to.",
	)
	flag.BoolVar(
		&flags.enableLeaderElection,
		"leader-elect",
		false,
		("Enable leader election for controller manager. " +
			"Enabling this will ensure there is only one active controller manager."),
	)
	flag.Parse()
}

func main() {
	var flags Flags
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	parseFlags(&flags)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        scheme,
		HealthProbeBindAddress:        flags.probeAddr,
		LeaderElection:                flags.enableLeaderElection,
		LeaderElectionID:              "0033bda7.slinky.slurm.net",
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to create kubeClient")
		os.Exit(1)
	}

	slurmClusters := resources.NewClusters()
	eventCh := make(chan event.GenericEvent, 100)
	if err = (&cluster.ClusterReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		SlurmClusters: slurmClusters,
		EventCh:       eventCh,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Cluster")
		os.Exit(1)
	}
	if err = (&nodeset.NodeSetReconciler{
		Client:        mgr.GetClient(),
		KubeClient:    kubeClient,
		Scheme:        mgr.GetScheme(),
		SlurmClusters: slurmClusters,
		EventCh:       eventCh,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "NodeSet")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder
	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running controller")
		os.Exit(1)
	}
}
