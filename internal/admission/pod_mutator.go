package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/bharathappali/jafra-controller/internal/annotations"
	"github.com/prometheus/client_golang/prometheus"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	profilerVolumeName  = "jafra-profiler"
	recordingVolumeName = "jafra-recordings"
	profilerInitName    = "jafra-profiler-init"
	profilerImage       = "quay.io/bharathappali/async-profiler:v4.5"
	profilerLibraryPath = "/jafra-agent/libasyncProfiler.so"
)

var (
	admissionRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "jafra_controller_admission_requests_total",
			Help: "Total Pod admission requests handled by Jafra.",
		},
		[]string{"result"},
	)
	mutationsPerformed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "jafra_controller_mutations_performed_total",
			Help: "Total Pod mutations performed by Jafra.",
		},
	)
	mutationsSkipped = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "jafra_controller_mutations_skipped_total",
			Help: "Total Pod mutations skipped by Jafra.",
		},
	)
	mutationFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "jafra_controller_mutation_failures_total",
			Help: "Total Jafra Pod mutation failures.",
		},
	)
	admissionLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "jafra_controller_admission_latency_seconds",
			Help:    "Time spent handling Jafra Pod admission requests.",
			Buckets: prometheus.DefBuckets,
		},
	)
)

func init() {
	metrics.Registry.MustRegister(
		admissionRequests,
		mutationsPerformed,
		mutationsSkipped,
		mutationFailures,
		admissionLatency,
	)
}

type PodMutator struct {
	version string
}

func NewPodMutator(version string) *PodMutator {
	return &PodMutator{version: version}
}

func (m *PodMutator) Handle(ctx context.Context, request cradmission.Request) cradmission.Response {
	started := time.Now()
	defer func() {
		admissionLatency.Observe(time.Since(started).Seconds())
	}()

	logger := log.FromContext(ctx).WithValues(
		"namespace", request.Namespace,
		"name", request.Name,
		"uid", request.UID,
	)

	if request.Operation != admissionv1.Create {
		admissionRequests.WithLabelValues("skipped").Inc()
		mutationsSkipped.Inc()
		return cradmission.Allowed("only Pod CREATE requests are handled")
	}

	var pod corev1.Pod
	if err := json.Unmarshal(request.Object.Raw, &pod); err != nil {
		admissionRequests.WithLabelValues("failed").Inc()
		mutationFailures.Inc()
		logger.Error(err, "unable to decode Pod admission request")
		return cradmission.Errored(400, fmt.Errorf("decode Pod: %w", err))
	}
	if pod.Namespace == "" {
		pod.Namespace = request.Namespace
	}

	selection, err := annotations.Select(&pod)
	if err != nil {
		admissionRequests.WithLabelValues("rejected").Inc()
		mutationFailures.Inc()
		logger.Info("rejecting invalid Jafra configuration", "reason", err.Error())
		return cradmission.Denied(err.Error())
	}
	if !selection.Eligible {
		admissionRequests.WithLabelValues("skipped").Inc()
		mutationsSkipped.Inc()
		return cradmission.Allowed("Pod did not require Jafra mutation")
	}

	profile, err := annotations.ResolveProfilerConfig(&pod)
	if err != nil {
		admissionRequests.WithLabelValues("rejected").Inc()
		mutationFailures.Inc()
		logger.Info("rejecting invalid profiler configuration", "reason", err.Error())
		return cradmission.Denied(err.Error())
	}
	if err := injectProfiler(&pod, selection, profile); err != nil {
		admissionRequests.WithLabelValues("rejected").Inc()
		mutationFailures.Inc()
		logger.Info("rejecting conflicting Pod configuration", "reason", err.Error())
		return cradmission.Denied(err.Error())
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[annotations.InjectedAnnotation] = annotations.EnabledValue
	pod.Annotations[annotations.InjectedVersionAnnotation] = m.version

	mutated, err := json.Marshal(&pod)
	if err != nil {
		admissionRequests.WithLabelValues("failed").Inc()
		mutationFailures.Inc()
		logger.Error(err, "unable to encode mutated Pod")
		return cradmission.Errored(500, fmt.Errorf("encode mutated Pod: %w", err))
	}

	admissionRequests.WithLabelValues("mutated").Inc()
	mutationsPerformed.Inc()
	logger.Info(
		"mutating opted-in Pod",
		"containers", selection.Containers,
		"controllerVersion", m.version,
	)
	return cradmission.PatchResponseFromRaw(request.Object.Raw, mutated)
}

func injectProfiler(
	pod *corev1.Pod,
	selection annotations.Selection,
	profile annotations.ProfilerConfig,
) error {
	emptyDirVolume := corev1.Volume{
		Name: profilerVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	recordingVolume := corev1.Volume{
		Name: recordingVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	if err := ensureVolume(pod, emptyDirVolume); err != nil {
		return err
	}
	if err := ensureVolume(pod, recordingVolume); err != nil {
		return err
	}

	initContainer := profilerInitContainer(selection.Containers)
	if err := ensureInitContainer(pod, initContainer); err != nil {
		return err
	}

	targets := make(map[string]struct{}, len(selection.Containers))
	for _, name := range selection.Containers {
		targets[name] = struct{}{}
	}
	for index := range pod.Spec.Containers {
		container := &pod.Spec.Containers[index]
		if _, selected := targets[container.Name]; !selected {
			continue
		}
		if err := mutateTargetContainer(container, profile); err != nil {
			return fmt.Errorf("container %q: %w", container.Name, err)
		}
	}
	return nil
}

func profilerInitContainer(targets []string) corev1.Container {
	allowPrivilegeEscalation := false
	readOnlyRootFilesystem := true
	return corev1.Container{
		Name:    profilerInitName,
		Image:   profilerImage,
		Command: []string{"/bin/sh", "-c"},
		Args: []string{`set -eu
library=/opt/async-profiler/lib/libasyncProfiler.so
if [ ! -f "${library}" ]; then
  echo "async-profiler library not found at ${library}" >&2
  exit 1
fi
echo "copying ${library} to ` + profilerLibraryPath + `"
cp "${library}" ` + profilerLibraryPath + `
chmod 0555 ` + profilerLibraryPath + `
old_ifs="${IFS}"
IFS=','
for container in ${JAFRA_TARGET_CONTAINERS}; do
  directory="/jafra-recordings/${JAFRA_NAMESPACE}/${JAFRA_POD_UID}/${container}"
  echo "creating recording directory ${directory}"
  mkdir -p "${directory}"
  chmod 0777 "${directory}"
  printf '%s\n' "{\"namespace\":\"${JAFRA_NAMESPACE}\",\"podName\":\"${JAFRA_POD_NAME}\",\"podUid\":\"${JAFRA_POD_UID}\",\"container\":\"${container}\"}" > "${directory}/.jafra-identity.json"
done
IFS="${old_ifs}"
echo "Jafra profiler initialization complete"`},
		Env: []corev1.EnvVar{
			downwardEnv("JAFRA_NAMESPACE", "metadata.namespace"),
			downwardEnv("JAFRA_POD_NAME", "metadata.name"),
			downwardEnv("JAFRA_POD_UID", "metadata.uid"),
			{Name: "JAFRA_TARGET_CONTAINERS", Value: strings.Join(targets, ",")},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: profilerVolumeName, MountPath: "/jafra-agent"},
			{Name: recordingVolumeName, MountPath: "/jafra-recordings"},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}
}

func mutateTargetContainer(
	container *corev1.Container,
	profile annotations.ProfilerConfig,
) error {
	environment := []corev1.EnvVar{
		downwardEnv("JAFRA_NAMESPACE", "metadata.namespace"),
		downwardEnv("JAFRA_POD_NAME", "metadata.name"),
		downwardEnv("JAFRA_POD_UID", "metadata.uid"),
		downwardEnv("JAFRA_NODE_NAME", "spec.nodeName"),
		{Name: "JAFRA_CONTAINER_NAME", Value: container.Name},
		{
			Name:  "JAFRA_RECORDING_RELATIVE_DIR",
			Value: "$(JAFRA_NAMESPACE)/$(JAFRA_POD_UID)/" + container.Name,
		},
	}
	for _, variable := range environment {
		if err := ensureEnv(container, variable); err != nil {
			return err
		}
	}

	mounts := []corev1.VolumeMount{
		{
			Name:      profilerVolumeName,
			MountPath: "/jafra-agent",
			ReadOnly:  true,
		},
		{
			Name:        recordingVolumeName,
			MountPath:   "/jfr-data",
			SubPathExpr: "$(JAFRA_RECORDING_RELATIVE_DIR)",
		},
	}
	for _, mount := range mounts {
		if err := ensureVolumeMount(container, mount); err != nil {
			return err
		}
	}

	return appendJavaToolOptions(container, profile.AgentOption(profilerLibraryPath))
}

func ensureVolume(pod *corev1.Pod, expected corev1.Volume) error {
	for _, existing := range pod.Spec.Volumes {
		if existing.Name != expected.Name {
			continue
		}
		if !reflect.DeepEqual(existing, expected) {
			return fmt.Errorf("volume %q already exists with incompatible configuration", expected.Name)
		}
		return nil
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, expected)
	return nil
}

func ensureInitContainer(pod *corev1.Pod, expected corev1.Container) error {
	for _, existing := range pod.Spec.InitContainers {
		if existing.Name != expected.Name {
			continue
		}
		if !reflect.DeepEqual(existing, expected) {
			return fmt.Errorf("init container %q already exists with incompatible configuration", expected.Name)
		}
		return nil
	}
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, expected)
	return nil
}

func ensureEnv(container *corev1.Container, expected corev1.EnvVar) error {
	for _, existing := range container.Env {
		if existing.Name != expected.Name {
			continue
		}
		if !reflect.DeepEqual(existing, expected) {
			return fmt.Errorf("environment variable %q already exists with incompatible configuration", expected.Name)
		}
		return nil
	}
	container.Env = append(container.Env, expected)
	return nil
}

func ensureVolumeMount(container *corev1.Container, expected corev1.VolumeMount) error {
	for _, existing := range container.VolumeMounts {
		if existing.Name != expected.Name && existing.MountPath != expected.MountPath {
			continue
		}
		if !reflect.DeepEqual(existing, expected) {
			return fmt.Errorf(
				"volume mount %q at %q conflicts with an existing mount",
				expected.Name,
				expected.MountPath,
			)
		}
		return nil
	}
	container.VolumeMounts = append(container.VolumeMounts, expected)
	return nil
}

func appendJavaToolOptions(container *corev1.Container, option string) error {
	for index := range container.Env {
		existing := &container.Env[index]
		if existing.Name != "JAVA_TOOL_OPTIONS" {
			continue
		}
		if existing.ValueFrom != nil {
			return fmt.Errorf("JAVA_TOOL_OPTIONS uses valueFrom and cannot be merged safely")
		}
		if strings.Contains(existing.Value, "-agentpath:"+profilerLibraryPath) {
			return nil
		}
		existing.Value = strings.TrimSpace(strings.TrimSpace(existing.Value) + " " + option)
		return nil
	}
	container.Env = append(container.Env, corev1.EnvVar{
		Name:  "JAVA_TOOL_OPTIONS",
		Value: option,
	})
	return nil
}

func downwardEnv(name, fieldPath string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{
				APIVersion: "v1",
				FieldPath:  fieldPath,
			},
		},
	}
}
