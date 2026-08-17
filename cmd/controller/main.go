package main

import (
	"crypto/tls"
	"os"

	"github.com/bharathappali/jafra-controller/internal/admission"
	"github.com/bharathappali/jafra-controller/internal/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func main() {
	ctrl.SetLogger(zap.New(zap.JSONEncoder()))
	logger := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		logger.Error(err, "unable to register Kubernetes core types")
		os.Exit(1)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		Port:    config.DefaultWebhookPort,
		CertDir: config.DefaultCertificateDir,
		TLSOpts: []func(*tls.Config){
			func(tlsConfig *tls.Config) {
				tlsConfig.MinVersion = tls.VersionTLS12
			},
		},
	})
	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: config.DefaultMetricsAddress},
		HealthProbeBindAddress: config.DefaultProbeAddress,
		WebhookServer:          webhookServer,
		LeaderElection:         false,
	})
	if err != nil {
		logger.Error(err, "unable to create controller manager")
		os.Exit(1)
	}

	manager.GetWebhookServer().Register(
		"/mutate-v1-pod",
		&cradmission.Webhook{Handler: admission.NewPodMutator(config.BuildVersion)},
	)
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to register health check")
		os.Exit(1)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to register readiness check")
		os.Exit(1)
	}

	logger.Info("starting Jafra admission controller", "version", config.BuildVersion)
	if err := manager.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "controller manager stopped")
		os.Exit(1)
	}
}
