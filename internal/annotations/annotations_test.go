package annotations

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const libraryPath = "/jafra-agent/libasyncProfiler.so"

func TestResolveProfilerConfigDefaults(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
	profile, err := ResolveProfilerConfig(pod)
	if err != nil {
		t.Fatalf("ResolveProfilerConfig: %v", err)
	}
	if profile.Profile != "medium" {
		t.Fatalf("profile = %q, want medium", profile.Profile)
	}
	if profile.NativeMemoryOption() != "-XX:NativeMemoryTracking=summary" {
		t.Fatalf("nmt option = %q", profile.NativeMemoryOption())
	}
	got := profile.AgentOption(libraryPath)
	want := "-agentpath:/jafra-agent/libasyncProfiler.so=start,event=ctimer,interval=20ms,wall=100ms,alloc=1m,lock=5ms,nativemem=2m,nativelock=5ms,memlimit=128m,loop=10m,chunktime=5s,chunksize=32m,jfrsync=/jafra-agent/jafra-medium.jfc,file=/jfr-data/profile-%n.jfr"
	if got != want {
		t.Fatalf("agent option =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, ",,") || strings.Contains(got, "all,") || strings.Contains(got, ",live,") {
		t.Fatalf("agent option must not contain empty tokens, bare all, or live: %s", got)
	}

	off, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{JfrSyncAnnotation: "none"},
	}})
	if err != nil {
		t.Fatalf("jfrsync=none: %v", err)
	}
	if strings.Contains(off.AgentOption(libraryPath), "jfrsync=") {
		t.Fatal("jfrsync=none must omit the option")
	}

	for _, builtin := range []string{"default", "profile"} {
		got, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{JfrSyncAnnotation: builtin},
		}})
		if err != nil {
			t.Fatalf("jfrsync=%s: %v", builtin, err)
		}
		want := "jfrsync=" + builtin + ","
		if !strings.Contains(got.AgentOption(libraryPath), want) {
			t.Fatalf("jfrsync=%s agent option = %s", builtin, got.AgentOption(libraryPath))
		}
	}
}

func TestResolveProfilerConfigPresets(t *testing.T) {
	tests := []struct {
		profile string
		want    string
	}{
		{
			profile: "aggressive",
			want:    "-agentpath:/jafra-agent/libasyncProfiler.so=start,event=ctimer,interval=10ms,wall=50ms,alloc=512k,lock=1ms,nativemem=1m,nativelock=1ms,memlimit=128m,loop=10m,chunktime=5s,chunksize=32m,jfrsync=/jafra-agent/jafra-aggressive.jfc,file=/jfr-data/profile-%n.jfr",
		},
		{
			profile: "low",
			want:    "-agentpath:/jafra-agent/libasyncProfiler.so=start,event=ctimer,interval=50ms,wall=200ms,alloc=4m,lock=20ms,nativelock=20ms,memlimit=128m,loop=15m,chunktime=5s,chunksize=32m,jfrsync=/jafra-agent/jafra-low.jfc,file=/jfr-data/profile-%n.jfr",
		},
	}
	for _, test := range tests {
		t.Run(test.profile, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				ProfileAnnotation: test.profile,
			}}}
			profile, err := ResolveProfilerConfig(pod)
			if err != nil {
				t.Fatalf("ResolveProfilerConfig: %v", err)
			}
			if got := profile.AgentOption(libraryPath); got != test.want {
				t.Fatalf("agent option =\n%s\nwant\n%s", got, test.want)
			}
			if strings.Contains(profile.AgentOption(libraryPath), "nativemem=") && test.profile == "low" {
				t.Fatal("low preset must omit nativemem")
			}
		})
	}
}

func TestResolveProfilerConfigOverrides(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		ProfileAnnotation:   "medium",
		LockAnnotation:      "10ms",
		AllocLiveAnnotation: "true",
		ChunkAgeAnnotation:  "15s",
	}}}
	profile, err := ResolveProfilerConfig(pod)
	if err != nil {
		t.Fatalf("ResolveProfilerConfig: %v", err)
	}
	got := profile.AgentOption(libraryPath)
	if !strings.Contains(got, ",live,") || !strings.Contains(got, "lock=10ms") || !strings.Contains(got, "chunktime=15s") {
		t.Fatalf("overrides were not applied: %s", got)
	}
	if !strings.Contains(got, "jfrsync=/jafra-agent/jafra-medium.jfc") {
		t.Fatalf("preset path missing: %s", got)
	}

	nmtOff, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		NmtAnnotation: "off",
	}}})
	if err != nil {
		t.Fatalf("nmt=off: %v", err)
	}
	if nmtOff.NativeMemoryOption() != "" {
		t.Fatalf("nmt=off injected %q", nmtOff.NativeMemoryOption())
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
		{"small chunk-age", map[string]string{ChunkAgeAnnotation: "1s"}, "at least 5s"},
		{"chunk annotations disagree", map[string]string{ChunkAgeAnnotation: "15s", ChunkTimeAnnotation: "30s"}, "disagree"},
		{"live annotations disagree", map[string]string{AllocLiveAnnotation: "true", LiveAnnotation: "false"}, "disagree"},
		{"unsafe jfrsync", map[string]string{JfrSyncAnnotation: "default,file=/tmp/x"}, JfrSyncAnnotation},
		{"jfrsync path", map[string]string{JfrSyncAnnotation: "/tmp/custom.jfc"}, JfrSyncAnnotation},
		{"unknown profile", map[string]string{ProfileAnnotation: "deep"}, ProfileAnnotation},
		{"unknown nmt", map[string]string{NmtAnnotation: "detail"}, NmtAnnotation},
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
