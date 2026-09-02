package config

const (
	// BuildVersion is recorded on every Pod mutated by this controller.
	BuildVersion = "0.0.2"

	DefaultMetricsAddress = ":8080"
	DefaultProbeAddress   = ":8081"
	DefaultWebhookPort    = 9443
	DefaultCertificateDir = "/tmp/k8s-webhook-server/serving-certs"
)
