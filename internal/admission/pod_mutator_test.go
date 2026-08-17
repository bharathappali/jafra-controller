package admission

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/evanphx/json-patch/v5"
	"github.com/bharathappali/jafra-controller/internal/annotations"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const testVersion = "0.1.0"

func TestPodMutator(t *testing.T) {
	tests := []struct {
		name            string
		pod             corev1.Pod
		wantAllowed     bool
		wantPatched     bool
		wantMessagePart string
	}{
		{
			name:        "unlabelled Pod",
			pod:         testPod("default", nil, nil, "app"),
			wantAllowed: true,
		},
		{
			name: "enabled continuous Pod",
			pod: testPod("default", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    annotations.ContinuousMode,
			}, map[string]string{annotations.ContainersAnnotation: "app"}, "app"),
			wantAllowed: true,
			wantPatched: true,
		},
		{
			name: "enabled Pod with unsupported mode",
			pod: testPod("default", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    "snapshot",
			}, map[string]string{annotations.ContainersAnnotation: "app"}, "app"),
			wantAllowed:     false,
			wantMessagePart: "unsupported",
		},
		{
			name: "already injected Pod",
			pod: testPod("default", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    annotations.ContinuousMode,
			}, map[string]string{
				annotations.InjectedAnnotation: annotations.EnabledValue,
			}, "app"),
			wantAllowed: true,
		},
		{
			name: "Pod in excluded namespace",
			pod: testPod("jafra-system", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    annotations.ContinuousMode,
			}, map[string]string{annotations.ContainersAnnotation: "app"}, "app"),
			wantAllowed: true,
		},
		{
			name: "Pod with missing target container annotation",
			pod: testPod("default", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    annotations.ContinuousMode,
			}, nil, "app"),
			wantAllowed:     false,
			wantMessagePart: annotations.ContainersAnnotation,
		},
		{
			name: "Pod with one valid target container",
			pod: testPod("default", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    annotations.ContinuousMode,
			}, map[string]string{annotations.ContainersAnnotation: "app"}, "app"),
			wantAllowed: true,
			wantPatched: true,
		},
		{
			name: "Pod with multiple valid target containers",
			pod: testPod("default", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    annotations.ContinuousMode,
			}, map[string]string{annotations.ContainersAnnotation: "app, worker"}, "app", "worker"),
			wantAllowed: true,
			wantPatched: true,
		},
		{
			name: "Pod with target container that does not exist",
			pod: testPod("default", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    annotations.ContinuousMode,
			}, map[string]string{annotations.ContainersAnnotation: "missing"}, "app"),
			wantAllowed:     false,
			wantMessagePart: "does not exist",
		},
	}

	mutator := NewPodMutator(testVersion)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, raw := admissionRequest(t, test.pod)
			response := mutator.Handle(context.Background(), request)

			if response.Allowed != test.wantAllowed {
				t.Fatalf("Allowed = %v, want %v; response: %#v", response.Allowed, test.wantAllowed, response)
			}
			if test.wantMessagePart != "" {
				if response.Result == nil || !strings.Contains(response.Result.Message, test.wantMessagePart) {
					t.Fatalf("message = %v, want it to contain %q", response.Result, test.wantMessagePart)
				}
			}
			if (len(response.Patches) > 0) != test.wantPatched {
				t.Fatalf("patch count = %d, wantPatched = %v", len(response.Patches), test.wantPatched)
			}
			if test.wantPatched {
				mutated := applyResponsePatch(t, raw, response)
				if got := mutated.Annotations[annotations.InjectedAnnotation]; got != annotations.EnabledValue {
					t.Errorf("%s = %q, want %q", annotations.InjectedAnnotation, got, annotations.EnabledValue)
				}
				if got := mutated.Annotations[annotations.InjectedVersionAnnotation]; got != testVersion {
					t.Errorf("%s = %q, want %q", annotations.InjectedVersionAnnotation, got, testVersion)
				}
			}
		})
	}
}

func TestProfilerMutation(t *testing.T) {
	runAsNonRoot := true
	originalSecurityContext := &corev1.PodSecurityContext{RunAsNonRoot: &runAsNonRoot}
	pod := testPod("default", map[string]string{
		annotations.EnabledLabel: annotations.EnabledValue,
		annotations.ModeLabel:    annotations.ContinuousMode,
	}, map[string]string{
		annotations.ContainersAnnotation: "app",
	}, "app", "worker")
	pod.Spec.SecurityContext = originalSecurityContext.DeepCopy()
	pod.Spec.InitContainers = []corev1.Container{
		{Name: "application-init", Image: "example.invalid/init:latest"},
	}
	pod.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "JAVA_TOOL_OPTIONS", Value: "-Xms256m -Xmx512m -Xlog:gc"},
	}
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: "application-data", MountPath: "/data"},
	}

	request, raw := admissionRequest(t, pod)
	response := NewPodMutator(testVersion).Handle(context.Background(), request)
	if !response.Allowed {
		t.Fatalf("expected mutation to be allowed: %#v", response.Result)
	}
	mutated := applyResponsePatch(t, raw, response)

	assertNamedCount(t, volumeNames(mutated.Spec.Volumes), profilerVolumeName, 1)
	assertNamedCount(t, volumeNames(mutated.Spec.Volumes), recordingVolumeName, 1)
	assertNamedCount(t, containerNames(mutated.Spec.InitContainers), profilerInitName, 1)
	assertNamedCount(t, containerNames(mutated.Spec.InitContainers), "application-init", 1)
	init := findContainer(t, mutated.Spec.InitContainers, profilerInitName)
	if !strings.Contains(strings.Join(init.Args, " "), ".jafra-identity.json") {
		t.Error("init container did not write .jafra-identity.json")
	}

	if !reflect.DeepEqual(mutated.Spec.SecurityContext, originalSecurityContext) {
		t.Errorf("Pod security context changed: got %#v, want %#v", mutated.Spec.SecurityContext, originalSecurityContext)
	}

	app := findContainer(t, mutated.Spec.Containers, "app")
	worker := findContainer(t, mutated.Spec.Containers, "worker")
	if findEnv(worker, "JAVA_TOOL_OPTIONS") != nil {
		t.Error("unselected worker container received JAVA_TOOL_OPTIONS")
	}
	if findEnv(worker, "JAFRA_POD_UID") != nil {
		t.Error("unselected worker container received Jafra environment")
	}
	wantAgent := "-agentpath:/jafra-agent/libasyncProfiler.so=start,event=ctimer,interval=20ms,wall=100ms,alloc=1m,live,lock=10ms,nativemem=2m,nativelock=10ms,memlimit=128m,loop=5m,chunktime=5s,chunksize=32m,jfrsync=default,file=/jfr-data/profile-%n.jfr"
	if got := findEnv(app, "JAVA_TOOL_OPTIONS"); got == nil ||
		!strings.Contains(got.Value, "-Xms256m -Xmx512m -Xlog:gc") ||
		!strings.Contains(got.Value, wantAgent) {
		t.Errorf("JAVA_TOOL_OPTIONS was not safely appended: %#v", got)
	}
	assertNamedCount(t, mountNames(app.VolumeMounts), "application-data", 1)
	assertNamedCount(t, mountNames(app.VolumeMounts), profilerVolumeName, 1)
	assertNamedCount(t, mountNames(app.VolumeMounts), recordingVolumeName, 1)

	selection, err := annotations.Select(&pod)
	if err != nil {
		t.Fatalf("select Pod: %v", err)
	}
	profile, err := annotations.ResolveProfilerConfig(&pod)
	if err != nil {
		t.Fatalf("resolve profiler config: %v", err)
	}
	if err := injectProfiler(&mutated, selection, profile); err != nil {
		t.Fatalf("repeat injection should be idempotent: %v", err)
	}
	assertNamedCount(t, volumeNames(mutated.Spec.Volumes), profilerVolumeName, 1)
	assertNamedCount(t, volumeNames(mutated.Spec.Volumes), recordingVolumeName, 1)
	assertNamedCount(t, containerNames(mutated.Spec.InitContainers), profilerInitName, 1)
	app = findContainer(t, mutated.Spec.Containers, "app")
	assertNamedCount(t, mountNames(app.VolumeMounts), profilerVolumeName, 1)
	assertNamedCount(t, mountNames(app.VolumeMounts), recordingVolumeName, 1)
	if got := strings.Count(findEnv(app, "JAVA_TOOL_OPTIONS").Value, "-agentpath:"+profilerLibraryPath); got != 1 {
		t.Errorf("profiler agent count = %d, want 1", got)
	}
}

func TestProfilerMutationRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name            string
		configure       func(*corev1.Pod)
		wantMessagePart string
	}{
		{
			name: "JAVA_TOOL_OPTIONS valueFrom",
			configure: func(pod *corev1.Pod) {
				pod.Spec.Containers[0].Env = []corev1.EnvVar{
					{
						Name: "JAVA_TOOL_OPTIONS",
						ValueFrom: &corev1.EnvVarSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "jvm-options"},
								Key:                  "options",
							},
						},
					},
				}
			},
			wantMessagePart: "valueFrom",
		},
		{
			name: "chunktime below async-profiler minimum",
			configure: func(pod *corev1.Pod) {
				pod.Annotations[annotations.ChunkTimeAnnotation] = "1s"
			},
			wantMessagePart: "at least 5s",
		},
		{
			name: "unsafe event separator",
			configure: func(pod *corev1.Pod) {
				pod.Annotations[annotations.EventAnnotation] = "wall,file=/tmp/escape.jfr"
			},
			wantMessagePart: annotations.EventAnnotation,
		},
		{
			name: "unsupported event",
			configure: func(pod *corev1.Pod) {
				pod.Annotations[annotations.EventAnnotation] = "cycles"
			},
			wantMessagePart: "unsupported",
		},
		{
			name: "invalid interval",
			configure: func(pod *corev1.Pod) {
				pod.Annotations[annotations.IntervalAnnotation] = "0ms"
			},
			wantMessagePart: annotations.IntervalAnnotation,
		},
		{
			name: "conflicting existing Jafra volume",
			configure: func(pod *corev1.Pod) {
				pod.Spec.Volumes = []corev1.Volume{
					{
						Name: profilerVolumeName,
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: "wrong"},
						},
					},
				}
			},
			wantMessagePart: "incompatible",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pod := testPod("default", map[string]string{
				annotations.EnabledLabel: annotations.EnabledValue,
				annotations.ModeLabel:    annotations.ContinuousMode,
			}, map[string]string{
				annotations.ContainersAnnotation: "app",
			}, "app")
			test.configure(&pod)
			request, _ := admissionRequest(t, pod)
			response := NewPodMutator(testVersion).Handle(context.Background(), request)
			if response.Allowed {
				t.Fatal("expected admission to be rejected")
			}
			if response.Result == nil || !strings.Contains(response.Result.Message, test.wantMessagePart) {
				t.Fatalf("message = %#v, want it to contain %q", response.Result, test.wantMessagePart)
			}
		})
	}
}

func testPod(
	namespace string,
	labels map[string]string,
	podAnnotations map[string]string,
	containers ...string,
) corev1.Pod {
	pod := corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-pod",
			Namespace:   namespace,
			Labels:      labels,
			Annotations: podAnnotations,
		},
	}
	for _, name := range containers {
		pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
			Name:  name,
			Image: "example.invalid/test:latest",
		})
	}
	return pod
}

func admissionRequest(t *testing.T, pod corev1.Pod) (cradmission.Request, []byte) {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("marshal Pod: %v", err)
	}
	return cradmission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:       types.UID("test-request"),
			Name:      pod.Name,
			Namespace: pod.Namespace,
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}, raw
}

func applyResponsePatch(t *testing.T, raw []byte, response cradmission.Response) corev1.Pod {
	t.Helper()
	patchBytes, err := json.Marshal(response.Patches)
	if err != nil {
		t.Fatalf("marshal JSON patch: %v", err)
	}
	patch, err := jsonpatch.DecodePatch(patchBytes)
	if err != nil {
		t.Fatalf("decode JSON patch: %v", err)
	}
	mutatedRaw, err := patch.Apply(raw)
	if err != nil {
		t.Fatalf("apply JSON patch: %v", err)
	}
	var mutated corev1.Pod
	if err := json.Unmarshal(mutatedRaw, &mutated); err != nil {
		t.Fatalf("decode mutated Pod: %v", err)
	}
	return mutated
}

func findContainer(t *testing.T, containers []corev1.Container, name string) *corev1.Container {
	t.Helper()
	for index := range containers {
		if containers[index].Name == name {
			return &containers[index]
		}
	}
	t.Fatalf("container %q not found", name)
	return nil
}

func findEnv(container *corev1.Container, name string) *corev1.EnvVar {
	for index := range container.Env {
		if container.Env[index].Name == name {
			return &container.Env[index]
		}
	}
	return nil
}

func volumeNames(volumes []corev1.Volume) []string {
	names := make([]string, 0, len(volumes))
	for _, volume := range volumes {
		names = append(names, volume.Name)
	}
	return names
}

func containerNames(containers []corev1.Container) []string {
	names := make([]string, 0, len(containers))
	for _, container := range containers {
		names = append(names, container.Name)
	}
	return names
}

func mountNames(mounts []corev1.VolumeMount) []string {
	names := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		names = append(names, mount.Name)
	}
	return names
}

func assertNamedCount(t *testing.T, names []string, name string, want int) {
	t.Helper()
	got := 0
	for _, candidate := range names {
		if candidate == name {
			got++
		}
	}
	if got != want {
		t.Errorf("%q count = %d, want %d; all names: %v", name, got, want, names)
	}
}
