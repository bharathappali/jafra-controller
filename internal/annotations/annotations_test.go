package annotations

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResolveProfilerConfigDefaults(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
	profile, err := ResolveProfilerConfig(pod)
	if err != nil {
		t.Fatalf("ResolveProfilerConfig: %v", err)
	}
	got := profile.AgentOption("/jafra-agent/libasyncProfiler.so")
	want := "-agentpath:/jafra-agent/libasyncProfiler.so=start,event=ctimer,interval=20ms,wall=100ms,alloc=1m,live,lock=10ms,nativemem=2m,nativelock=10ms,memlimit=128m,loop=5m,chunktime=5s,chunksize=32m,jfrsync=default,file=/jfr-data/profile-%n.jfr"
	if got != want {
		t.Fatalf("agent option =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, ",,") || strings.Contains(got, "all,") {
		t.Fatalf("agent option must not contain empty tokens or bare all: %s", got)
	}

	off, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{JfrSyncAnnotation: "none"},
	}})
	if err != nil {
		t.Fatalf("jfrsync=none: %v", err)
	}
	if strings.Contains(off.AgentOption("/jafra-agent/libasyncProfiler.so"), "jfrsync=") {
		t.Fatal("jfrsync=none must omit the option")
	}
}

func TestResolveProfilerConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        string
	}{
		{"unsupported event", map[string]string{EventAnnotation: "cycles"}, "unsupported"},
		{"unsafe event", map[string]string{EventAnnotation: "ctimer,file=/tmp/x"}, EventAnnotation},
		{"zero interval", map[string]string{IntervalAnnotation: "0ms"}, IntervalAnnotation},
		{"invalid live", map[string]string{LiveAnnotation: "yes"}, LiveAnnotation},
		{"small chunktime", map[string]string{ChunkTimeAnnotation: "1s"}, "at least 5s"},
		{"unsafe jfrsync", map[string]string{JfrSyncAnnotation: "default,file=/tmp/x"}, JfrSyncAnnotation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: test.annotations}}
			_, err := ResolveProfilerConfig(pod)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}
