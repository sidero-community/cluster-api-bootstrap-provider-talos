// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	cgrecord "k8s.io/client-go/tools/record"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/klog/v2"
	capiv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util/flags"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	bootstrapv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/controllers"
	"github.com/siderolabs/cluster-api-bootstrap-provider-talos/internal/inplace"
	// +kubebuilder:scaffold:imports
)

var (
	setupLog = ctrl.Log.WithName("setup")

	healthAddr           string
	enableLeaderElection bool
	webhookPort          int
	watchFilterValue     string
	webhookCertDir       string
	managerOptions       = flags.ManagerOptions{}
	logOptions           = logs.NewOptions()

	enableRuntimeExtension  bool
	runtimeExtensionPort    int
	runtimeExtensionCertDir string
)

func InitFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(logOptions, fs)

	fs.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")

	fs.IntVar(&webhookPort, "webhook-port", 9443,
		"Webhook Server port, disabled by default. When enabled, the manager will only work as webhook server, no reconcilers are installed.")

	fs.StringVar(&webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs/",
		"Webhook cert dir, only used when webhook-port is specified.")

	fs.StringVar(&watchFilterValue, "watch-filter", "",
		fmt.Sprintf("Label value that the controller watches to reconcile cluster-api objects. Label key is always %s. If unspecified, the controller watches for all cluster-api objects.", capiv1.WatchLabel))

	fs.StringVar(&healthAddr, "health-addr", ":9440",
		"The address the health endpoint binds to.")

	fs.BoolVar(&enableRuntimeExtension, "enable-runtime-extension", true,
		"Serve the Cluster API in-place update hooks (CanUpdateMachine, CanUpdateMachineSet, UpdateMachine). "+
			"The server needs a serving certificate at --runtime-extension-cert-dir, which the shipped manifests mount, "+
			"and the manager exits if it is absent. Set to false when running without those manifests. "+
			"Cluster API only calls the hooks once the InPlaceUpdates feature gate is on and an ExtensionConfig points at this server.")

	fs.IntVar(&runtimeExtensionPort, "runtime-extension-port", 9445,
		"Port the runtime extension server binds to, only used when --enable-runtime-extension is set.")

	fs.StringVar(&runtimeExtensionCertDir, "runtime-extension-cert-dir", "/tmp/k8s-runtime-extension-server/serving-certs/",
		"Directory holding tls.crt and tls.key for the runtime extension server, only used when --enable-runtime-extension is set.")

	flags.AddManagerOptions(fs, &managerOptions)

	feature.MutableGates.AddFlag(fs)
}

func main() {
	InitFlags(pflag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	if err := logsv1.ValidateAndApply(logOptions, nil); err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// klog.Background will automatically use the right logger.
	ctrl.SetLogger(klog.Background())

	// Machine and cluster operations can create enough events to trigger the event recorder spam filter
	// Setting the burst size higher ensures all events will be recorded and submitted to the API
	broadcaster := cgrecord.NewBroadcasterWithCorrelatorOptions(cgrecord.CorrelatorOptions{
		BurstSize: 100,
	})

	tlsOptions, metricOpts, err := flags.GetManagerOptions(managerOptions)
	if err != nil {
		setupLog.Error(err, "unable to get manager options")
		os.Exit(1)
	}

	req, _ := labels.NewRequirement(capiv1.ClusterNameLabel, selection.Exists, nil)
	clusterSecretCacheSelector := labels.NewSelector().Add(*req)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "controller-leader-election-cabpt",
		Metrics:                *metricOpts,
		EventBroadcaster:       broadcaster,
		HealthProbeBindAddress: healthAddr,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				// Note: Only Secrets with the cluster name label are cached.
				// The default client of the manager won't use the cache for secrets at all (see Client.Cache.DisableFor).
				// The cached secrets will only be used by the secretCachingClient we create below.
				&corev1.Secret{}: {
					Label: clusterSecretCacheSelector,
				},
			},
		},
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{
					&corev1.ConfigMap{},
					&corev1.Secret{},
				},
			},
		},
		WebhookServer: webhook.NewServer(
			webhook.Options{
				Port:    webhookPort,
				CertDir: webhookCertDir,
				TLSOpts: tlsOptions,
			},
		),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	setupReconcilers(ctx, mgr)
	setupWebhooks(mgr)
	setupChecks(mgr)

	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setupReconcilers(ctx context.Context, mgr manager.Manager) {
	if err := (&controllers.TalosConfigReconciler{
		Client:           mgr.GetClient(),
		Log:              ctrl.Log.WithName("controllers").WithName("TalosConfig"),
		Scheme:           mgr.GetScheme(),
		WatchFilterValue: watchFilterValue,
	}).SetupWithManager(ctx, mgr, controller.Options{MaxConcurrentReconciles: 10}); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TalosConfig")
		os.Exit(1)
	}

	if enableRuntimeExtension {
		setupLog.Info("enabling the in-place update runtime extension", "port", runtimeExtensionPort)

		handler := inplace.NewHandler(mgr.GetClient(), inplace.NewNodeClient(mgr.GetClient()))

		if err := inplace.AddToManager(mgr, handler, inplace.Options{
			Port:    runtimeExtensionPort,
			CertDir: runtimeExtensionCertDir,
		}); err != nil {
			setupLog.Error(err, "unable to start the in-place update runtime extension")
			os.Exit(1)
		}
	}
}

func setupWebhooks(mgr manager.Manager) {
	if err := (&bootstrapv1beta1.TalosConfigTemplate{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "TalosConfigTemplate")
		os.Exit(1)
	}
	if err := (&bootstrapv1beta1.TalosConfig{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "TalosConfig")
		os.Exit(1)
	}
}

func setupChecks(mgr ctrl.Manager) {
	if err := mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		setupLog.Error(err, "unable to create ready check")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		setupLog.Error(err, "unable to create health check")
		os.Exit(1)
	}
}
