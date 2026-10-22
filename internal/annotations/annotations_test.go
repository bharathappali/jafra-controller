package annotations

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const libraryPath = "/jafra-agent/libasyncProfiler.so"

const (
	lowAgentOption        = "-agentpath:/jafra-agent/libasyncProfiler.so=start,event=ctimer,interval=20ms,wall=200ms,alloc=4m,lock=10ms,nativemem=64m,nofree,cstack=vm,jstackdepth=512,memlimit=16m,loop=5s,jfrsync=/jafra-agent/jafra-low.jfc,file=/jfr-data/profile-%n.jfr"
	mediumAgentOption     = "-agentpath:/jafra-agent/libasyncProfiler.so=start,event=ctimer,interval=10ms,wall=100ms,alloc=1m,lock=1ms,nativemem=16m,nofree,cstack=vm,jstackdepth=512,memlimit=32m,loop=5s,jfrsync=/jafra-agent/jafra-medium.jfc,file=/jfr-data/profile-%n.jfr"
	aggressiveAgentOption = "-agentpath:/jafra-agent/libasyncProfiler.so=start,event=ctimer,interval=10ms,wall=50ms,alloc=512k,lock=100us,nativemem=2m,nofree,cstack=vm,jstackdepth=1024,memlimit=64m,loop=5s,jfrsync=/jafra-agent/jafra-aggressive.jfc,file=/jfr-data/profile-%n.jfr"
)

func TestResolveProfilerConfigDefaults(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
	profile, warnings, err := ResolveProfilerConfig(pod)
	if err != nil {
		t.Fatalf("ResolveProfilerConfig: %v", err)
	}
	if profile.Profile != "medium" {
		t.Fatalf("profile = %q, want medium", profile.Profile)
	}
	if profile.LatencyThreshold != "500 ms" {
		t.Fatalf("latency threshold = %q, want 500 ms", profile.LatencyThreshold)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}
	if profile.JdkNmt || profile.NativeMemoryOption() != "" {
		t.Fatalf("NMT = %v option %q, want off", profile.JdkNmt, profile.NativeMemoryOption())
	}
	got := profile.AgentOption(libraryPath)
	if got != mediumAgentOption {
		t.Fatalf("agent option =\n%s\nwant\n%s", got, mediumAgentOption)
	}
	if strings.Contains(got, ",,") {
		t.Fatalf("agent option must not contain empty tokens: %s", got)
	}

	off, _, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{JfrSyncAnnotation: "none"},
	}})
	if err != nil {
		t.Fatalf("jfrsync=none: %v", err)
	}
	if strings.Contains(off.AgentOption(libraryPath), "jfrsync=") {
		t.Fatal("jfrsync=none must omit the option")
	}

	for _, builtin := range []string{"default", "profile"} {
		got, _, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
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
		{profile: "low", want: lowAgentOption},
		{profile: "medium", want: mediumAgentOption},
		{profile: "aggressive", want: aggressiveAgentOption},
	}
	for _, test := range tests {
		t.Run(test.profile, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				ProfileAnnotation: test.profile,
			}}}
			profile, _, err := ResolveProfilerConfig(pod)
			if err != nil {
				t.Fatalf("ResolveProfilerConfig: %v", err)
			}
			if got := profile.AgentOption(libraryPath); got != test.want {
				t.Fatalf("agent option =\n%s\nwant\n%s", got, test.want)
			}
		})
	}
}

func TestResolveProfilerConfigOverrides(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		ProfileAnnotation: "medium",
		LockAnnotation:    "10ms",
	}}}
	profile, warnings, err := ResolveProfilerConfig(pod)
	if err != nil {
		t.Fatalf("ResolveProfilerConfig: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}
	got := profile.AgentOption(libraryPath)
	if !strings.Contains(got, "lock=10ms") {
		t.Fatalf("overrides were not applied: %s", got)
	}
	if !strings.Contains(got, "jfrsync=/jafra-agent/jafra-medium.jfc") {
		t.Fatalf("preset path missing: %s", got)
	}
}

func TestJdkNmt(t *testing.T) {
	absent, warnings, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}})
	if err != nil {
		t.Fatalf("absent: %v", err)
	}
	if absent.JdkNmt || absent.NativeMemoryOption() != "" || len(warnings) != 0 {
		t.Fatalf("absent NMT = %v option %q warnings %#v", absent.JdkNmt, absent.NativeMemoryOption(), warnings)
	}

	for _, value := range []string{"enabled", " Enabled "} {
		profile, warnings, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			JdkNmtAnnotation: value,
		}}})
		if err != nil {
			t.Fatalf("jdk-nmt=%q: %v", value, err)
		}
		if !profile.JdkNmt || profile.NativeMemoryOption() != "-XX:NativeMemoryTracking=summary" || len(warnings) != 0 {
			t.Fatalf("jdk-nmt=%q NMT = %v option %q warnings %#v", value, profile.JdkNmt, profile.NativeMemoryOption(), warnings)
		}
	}

	for _, value := range []string{"true", "summary", "off", ""} {
		profile, warnings, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			JdkNmtAnnotation: value,
		}}})
		if err != nil {
			t.Fatalf("jdk-nmt=%q: %v", value, err)
		}
		want := fmt.Sprintf("%s value %q is not \"enabled\"; NMT stays off", JdkNmtAnnotation, value)
		if profile.JdkNmt || profile.NativeMemoryOption() != "" || len(warnings) != 1 || warnings[0] != want {
			t.Fatalf("jdk-nmt=%q NMT = %v option %q warnings %#v, want [%q]", value, profile.JdkNmt, profile.NativeMemoryOption(), warnings, want)
		}
	}

	legacy, warnings, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		"jafra.io/nmt": "summary",
	}}})
	if err != nil {
		t.Fatalf("legacy nmt: %v", err)
	}
	want := "jafra.io/nmt is no longer supported and was ignored"
	if legacy.JdkNmt || legacy.NativeMemoryOption() != "" || len(warnings) != 1 || warnings[0] != want {
		t.Fatalf("legacy NMT = %v option %q warnings %#v", legacy.JdkNmt, legacy.NativeMemoryOption(), warnings)
	}
}

func TestNativeMemOffDropsNofree(t *testing.T) {
	profile, _, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		NativeMemAnnotation: "off",
	}}})
	if err != nil {
		t.Fatalf("nativemem=off: %v", err)
	}
	got := profile.AgentOption(libraryPath)
	if strings.Contains(got, "nativemem=") || strings.Contains(got, "nofree") {
		t.Fatalf("nativemem=off must drop nativemem and nofree: %s", got)
	}
}

func TestLockMicrosecondsAccepted(t *testing.T) {
	profile, _, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		LockAnnotation: "100us",
	}}})
	if err != nil {
		t.Fatalf("lock=100us: %v", err)
	}
	if !strings.Contains(profile.AgentOption(libraryPath), "lock=100us") {
		t.Fatalf("lock=100us was rejected or dropped: %s", profile.AgentOption(libraryPath))
	}
}

func TestRemovedAnnotationsWarnAndAreIgnored(t *testing.T) {
	for _, key := range removedAnnotations {
		t.Run(key, func(t *testing.T) {
			profile, warnings, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				key: "1s",
			}}})
			if err != nil {
				t.Fatalf("ResolveProfilerConfig: %v", err)
			}
			if got := profile.AgentOption(libraryPath); got != mediumAgentOption {
				t.Fatalf("agent option =\n%s\nwant\n%s", got, mediumAgentOption)
			}
			want := key + " is no longer supported and was ignored"
			if len(warnings) != 1 || warnings[0] != want {
				t.Fatalf("warnings = %#v, want [%q]", warnings, want)
			}
		})
	}
}

func TestLatencyThreshold(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		warning string
	}{
		{name: "compact milliseconds", value: "500ms", want: "500 ms"},
		{name: "one second", value: "1 s", want: "1000 ms"},
		{name: "two minutes", value: "2m", want: "120000 ms"},
		{name: "floor", value: "100 ms", want: "100 ms"},
		{name: "microseconds that do not divide into milliseconds", value: "150500 us", want: "150500 us"},
		{name: "nanoseconds that do not divide into microseconds", value: "100000001 ns", want: "100000001 ns"},
		{
			name:    "below floor milliseconds",
			value:   "99 ms",
			want:    "100 ms",
			warning: `jafra.io/latency-threshold value "99 ms" is below the 100 ms floor; using 100 ms`,
		},
		{
			name:    "zero",
			value:   "0 ms",
			want:    "100 ms",
			warning: `jafra.io/latency-threshold value "0 ms" is below the 100 ms floor; using 100 ms`,
		},
		{
			name:    "below floor microseconds",
			value:   "50000 us",
			want:    "100 ms",
			warning: `jafra.io/latency-threshold value "50000 us" is below the 100 ms floor; using 100 ms`,
		},
		{
			name:    "words",
			value:   "abc",
			want:    "500 ms",
			warning: `jafra.io/latency-threshold value "abc" is not a JFR timespan; using 500 ms`,
		},
		{
			name:    "decimal",
			value:   "1.5 s",
			want:    "500 ms",
			warning: `jafra.io/latency-threshold value "1.5 s" is not a JFR timespan; using 500 ms`,
		},
		{
			name:    "negative",
			value:   "-5 ms",
			want:    "500 ms",
			warning: `jafra.io/latency-threshold value "-5 ms" is not a JFR timespan; using 500 ms`,
		},
		{
			name:    "unknown unit",
			value:   "5 parsecs",
			want:    "500 ms",
			warning: `jafra.io/latency-threshold value "5 parsecs" is not a JFR timespan; using 500 ms`,
		},
		{
			name:    "empty",
			value:   "",
			want:    "500 ms",
			warning: `jafra.io/latency-threshold value "" is not a JFR timespan; using 500 ms`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, warnings, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				LatencyThresholdAnnotation: test.value,
			}}})
			if err != nil {
				t.Fatalf("ResolveProfilerConfig: %v", err)
			}
			if profile.LatencyThreshold != test.want {
				t.Fatalf("threshold = %q, want %q", profile.LatencyThreshold, test.want)
			}
			if test.warning == "" {
				if len(warnings) != 0 {
					t.Fatalf("warnings = %#v, want none", warnings)
				}
				return
			}
			if len(warnings) != 1 || warnings[0] != test.warning {
				t.Fatalf("warnings = %#v, want [%q]", warnings, test.warning)
			}
		})
	}
}

func TestLatencyThresholdIgnoredWithoutJafraPreset(t *testing.T) {
	profile, warnings, err := ResolveProfilerConfig(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		JfrSyncAnnotation:          "default",
		LatencyThresholdAnnotation: "500ms",
	}}})
	if err != nil {
		t.Fatalf("ResolveProfilerConfig: %v", err)
	}
	if profile.LatencyThreshold != "500 ms" {
		t.Fatalf("threshold = %q", profile.LatencyThreshold)
	}
	want := LatencyThresholdAnnotation + " has no effect unless the Jafra JFC preset is used"
	if len(warnings) != 1 || warnings[0] != want {
		t.Fatalf("warnings = %#v, want [%q]", warnings, want)
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
		{"unsafe jfrsync", map[string]string{JfrSyncAnnotation: "default,file=/tmp/x"}, JfrSyncAnnotation},
		{"jfrsync path", map[string]string{JfrSyncAnnotation: "/tmp/custom.jfc"}, JfrSyncAnnotation},
		{"unknown profile", map[string]string{ProfileAnnotation: "deep"}, ProfileAnnotation},
		{"unknown profile high", map[string]string{ProfileAnnotation: "high"}, ProfileAnnotation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: test.annotations}}
			_, _, err := ResolveProfilerConfig(pod)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}
