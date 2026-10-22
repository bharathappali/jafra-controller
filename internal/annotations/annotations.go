package annotations

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	EnabledLabel               = "jafra.io/enabled"
	ModeLabel                  = "jafra.io/mode"
	ContainersAnnotation       = "jafra.io/containers"
	EventAnnotation            = "jafra.io/event"
	IntervalAnnotation         = "jafra.io/interval"
	WallAnnotation             = "jafra.io/wall"
	AllocAnnotation            = "jafra.io/alloc"
	LiveAnnotation             = "jafra.io/live"
	LockAnnotation             = "jafra.io/lock"
	NativeMemAnnotation        = "jafra.io/nativemem"
	NativeLockAnnotation       = "jafra.io/nativelock"
	MemLimitAnnotation         = "jafra.io/memlimit"
	LoopAnnotation             = "jafra.io/loop"
	ChunkTimeAnnotation        = "jafra.io/chunktime"
	ChunkAgeAnnotation         = "jafra.io/chunk-age"
	ChunkSizeAnnotation        = "jafra.io/chunksize"
	JfrSyncAnnotation          = "jafra.io/jfrsync"
	ProfileAnnotation          = "jafra.io/profile"
	JdkNmtAnnotation           = "jafra.io/jdk-nmt"
	LatencyThresholdAnnotation = "jafra.io/latency-threshold"
	AllocLiveAnnotation        = "jafra.io/alloc-live"
	InjectedAnnotation         = "jafra.io/injected"
	InjectedVersionAnnotation  = "jafra.io/injected-version"

	EnabledValue   = "true"
	ContinuousMode = "continuous"

	DefaultEvent            = "ctimer"
	DefaultInterval         = "10ms"
	DefaultWall             = "100ms"
	DefaultAlloc            = "1m"
	DefaultLock             = "1ms"
	DefaultNativeMem        = "16m"
	DefaultMemLimit         = "32m"
	DefaultLoop             = "5s"
	DefaultProfile          = "medium"
	DefaultLatencyThreshold = "500 ms"

	jfcDirectory         = "/jafra-agent"
	nativeMemoryTracking = "-XX:NativeMemoryTracking=summary"
)

var (
	eventPattern     = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	durationPattern  = regexp.MustCompile(`^([1-9][0-9]*)(ns|us|ms|s|m|h|d)$`)
	latencyPattern   = regexp.MustCompile(`^([0-9]+)[ ]*(ns|us|ms|s|m|h|d)$`)
	sizePattern      = regexp.MustCompile(`^[1-9][0-9]*[kmg]$`)
	latencyUnitNanos = map[string]uint64{
		"ns": 1,
		"us": 1000,
		"ms": 1000 * 1000,
		"s":  1000 * 1000 * 1000,
		"m":  60 * 1000 * 1000 * 1000,
		"h":  60 * 60 * 1000 * 1000 * 1000,
		"d":  24 * 60 * 60 * 1000 * 1000 * 1000,
	}
	allowedEvents = map[string]struct{}{
		"ctimer": {},
		"cpu":    {},
		"wall":   {},
		"itimer": {},
	}
	allowedProfiles = map[string]struct{}{
		"aggressive": {},
		"medium":     {},
		"low":        {},
	}
	jfrSyncDisabled = map[string]struct{}{
		"none":  {},
		"false": {},
		"off":   {},
	}
	jfrSyncBuiltin = map[string]struct{}{
		"default": {},
		"profile": {},
	}
	// removedAnnotations are ignored. Each one produces a warning.
	removedAnnotations = []string{
		NativeLockAnnotation,
		ChunkTimeAnnotation,
		ChunkAgeAnnotation,
		ChunkSizeAnnotation,
		LiveAnnotation,
		AllocLiveAnnotation,
		LoopAnnotation,
		"jafra.io/nmt",
	}
	presetKnobs = map[string]ProfilerConfig{
		"aggressive": {
			Event: "ctimer", Interval: "10ms", Wall: "50ms", Alloc: "512k",
			Lock: "100us", NativeMem: "2m", JStackDepth: 1024,
			MemLimit: "64m", Loop: "5s",
		},
		"medium": {
			Event: "ctimer", Interval: "10ms", Wall: "100ms", Alloc: "1m",
			Lock: "1ms", NativeMem: "16m", JStackDepth: 512,
			MemLimit: "32m", Loop: "5s",
		},
		"low": {
			Event: "ctimer", Interval: "20ms", Wall: "200ms", Alloc: "4m",
			Lock: "10ms", NativeMem: "64m", JStackDepth: 512,
			MemLimit: "16m", Loop: "5s",
		},
	}
)

type Selection struct {
	Eligible   bool
	Containers []string
}

type ProfilerConfig struct {
	Profile          string
	Event            string
	Interval         string
	Wall             string
	Alloc            string
	Lock             string
	NativeMem        string
	JStackDepth      int
	MemLimit         string
	Loop             string
	JfrSync          string
	JdkNmt           bool
	LatencyThreshold string
}

// Select returns the target containers for a Pod that explicitly opts in.
func Select(pod *corev1.Pod) (Selection, error) {
	if pod.Namespace == "kube-system" || pod.Namespace == "jafra-system" {
		return Selection{}, nil
	}
	if pod.Labels[EnabledLabel] != EnabledValue {
		return Selection{}, nil
	}

	mode := pod.Labels[ModeLabel]
	if mode == "" {
		return Selection{}, nil
	}
	if mode != ContinuousMode {
		return Selection{}, fmt.Errorf("unsupported %s value %q", ModeLabel, mode)
	}
	if pod.Annotations[InjectedAnnotation] == EnabledValue {
		return Selection{}, nil
	}

	rawTargets, ok := pod.Annotations[ContainersAnnotation]
	if !ok || strings.TrimSpace(rawTargets) == "" {
		return Selection{}, fmt.Errorf("%s must name at least one container", ContainersAnnotation)
	}

	available := make(map[string]struct{}, len(pod.Spec.Containers))
	for _, container := range pod.Spec.Containers {
		available[container.Name] = struct{}{}
	}

	seen := make(map[string]struct{})
	targets := make([]string, 0)
	for _, rawTarget := range strings.Split(rawTargets, ",") {
		target := strings.TrimSpace(rawTarget)
		if target == "" {
			return Selection{}, fmt.Errorf("%s contains an empty container name", ContainersAnnotation)
		}
		if _, exists := available[target]; !exists {
			return Selection{}, fmt.Errorf("target container %q does not exist", target)
		}
		if _, duplicate := seen[target]; duplicate {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}

	return Selection{Eligible: true, Containers: targets}, nil
}

// ResolveProfilerConfig applies the selected JFC preset, then any explicit
// per-knob annotations. loop stays at 5s. Removed annotations are ignored
// and returned as warnings.
func ResolveProfilerConfig(pod *corev1.Pod) (ProfilerConfig, []string, error) {
	profileName := strings.ToLower(valueOrDefault(pod.Annotations, ProfileAnnotation, DefaultProfile))
	if _, allowed := allowedProfiles[profileName]; !allowed {
		return ProfilerConfig{}, nil, fmt.Errorf("unsupported %s value %q", ProfileAnnotation, profileName)
	}
	preset := presetKnobs[profileName]
	annotations := pod.Annotations

	profile := ProfilerConfig{
		Profile:     profileName,
		Event:       overrideOr(annotations, EventAnnotation, preset.Event),
		Interval:    overrideOr(annotations, IntervalAnnotation, preset.Interval),
		Wall:        overrideOr(annotations, WallAnnotation, preset.Wall),
		Alloc:       overrideOr(annotations, AllocAnnotation, preset.Alloc),
		Lock:        overrideOr(annotations, LockAnnotation, preset.Lock),
		NativeMem:   overrideOr(annotations, NativeMemAnnotation, preset.NativeMem),
		JStackDepth: preset.JStackDepth,
		MemLimit:    overrideOr(annotations, MemLimitAnnotation, preset.MemLimit),
		Loop:        preset.Loop,
	}

	jdkNmt, nmtWarnings := resolveJdkNmt(annotations)
	profile.JdkNmt = jdkNmt
	warnings := append(removedAnnotationWarnings(annotations), nmtWarnings...)

	jfrSync, err := resolveJfrSync(annotations, profileName)
	if err != nil {
		return ProfilerConfig{}, nil, err
	}
	profile.JfrSync = jfrSync

	if len(profile.Event) > 64 || !eventPattern.MatchString(profile.Event) {
		return ProfilerConfig{}, nil, fmt.Errorf("invalid %s value %q", EventAnnotation, profile.Event)
	}
	if _, allowed := allowedEvents[profile.Event]; !allowed {
		return ProfilerConfig{}, nil, fmt.Errorf("unsupported %s value %q", EventAnnotation, profile.Event)
	}
	if err := requireDuration(IntervalAnnotation, profile.Interval); err != nil {
		return ProfilerConfig{}, nil, err
	}
	if err := requireDuration(WallAnnotation, profile.Wall); err != nil {
		return ProfilerConfig{}, nil, err
	}
	if err := requireDuration(LockAnnotation, profile.Lock); err != nil {
		return ProfilerConfig{}, nil, err
	}
	if err := requireSize(AllocAnnotation, profile.Alloc); err != nil {
		return ProfilerConfig{}, nil, err
	}
	if strings.EqualFold(profile.NativeMem, "off") {
		profile.NativeMem = ""
	} else if profile.NativeMem != "" {
		if err := requireSize(NativeMemAnnotation, profile.NativeMem); err != nil {
			return ProfilerConfig{}, nil, err
		}
	}
	if err := requireSize(MemLimitAnnotation, profile.MemLimit); err != nil {
		return ProfilerConfig{}, nil, err
	}

	threshold, latencyWarnings := resolveLatencyThreshold(annotations, profile.JfrSync)
	profile.LatencyThreshold = threshold
	warnings = append(warnings, latencyWarnings...)

	return profile, warnings, nil
}

func (c ProfilerConfig) AgentOption(libraryPath string) string {
	parts := []string{
		"start",
		"event=" + c.Event,
		"interval=" + c.Interval,
		"wall=" + c.Wall,
		"alloc=" + c.Alloc,
		"lock=" + c.Lock,
	}
	if c.NativeMem != "" {
		parts = append(parts, "nativemem="+c.NativeMem, "nofree")
	}
	parts = append(parts,
		"cstack=vm",
		"jstackdepth="+strconv.Itoa(c.JStackDepth),
		"memlimit="+c.MemLimit,
		"loop="+c.Loop,
	)
	if c.JfrSync != "" {
		parts = append(parts, "jfrsync="+c.JfrSync)
	}
	parts = append(parts, "file=/jfr-data/profile-%n.jfr")
	return "-agentpath:" + libraryPath + "=" + strings.Join(parts, ",")
}

// NativeMemoryOption is the JVM flag injected when jafra.io/jdk-nmt is enabled.
// An empty string means the webhook must not inject the flag.
func (c ProfilerConfig) NativeMemoryOption() string {
	if c.JdkNmt {
		return nativeMemoryTracking
	}
	return ""
}

func resolveJdkNmt(annotations map[string]string) (bool, []string) {
	raw, present := annotationValue(annotations, JdkNmtAnnotation)
	if !present {
		return false, nil
	}
	if strings.EqualFold(raw, "enabled") {
		return true, nil
	}
	return false, []string{fmt.Sprintf("%s value %q is not \"enabled\"; NMT stays off", JdkNmtAnnotation, raw)}
}

func removedAnnotationWarnings(annotations map[string]string) []string {
	var warnings []string
	for _, key := range removedAnnotations {
		if _, present := annotationValue(annotations, key); present {
			warnings = append(warnings, fmt.Sprintf("%s is no longer supported and was ignored", key))
		}
	}
	return warnings
}

func resolveJfrSync(annotations map[string]string, profileName string) (string, error) {
	raw, present := annotationValue(annotations, JfrSyncAnnotation)
	if !present {
		return jfcDirectory + "/jafra-" + profileName + ".jfc", nil
	}
	value := strings.ToLower(raw)
	if _, disabled := jfrSyncDisabled[value]; disabled {
		return "", nil
	}
	if _, builtin := jfrSyncBuiltin[value]; builtin {
		return value, nil
	}
	return "", fmt.Errorf("unsupported %s value %q", JfrSyncAnnotation, raw)
}

func overrideOr(annotations map[string]string, key, preset string) string {
	if value, present := annotationValue(annotations, key); present {
		return value
	}
	return preset
}

func annotationValue(annotations map[string]string, key string) (string, bool) {
	if annotations == nil {
		return "", false
	}
	value, present := annotations[key]
	if !present {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func valueOrDefault(values map[string]string, key, defaultValue string) string {
	if value, exists := values[key]; exists {
		return strings.TrimSpace(value)
	}
	return defaultValue
}

func requireDuration(annotation, value string) error {
	if !durationPattern.MatchString(value) {
		return fmt.Errorf("invalid %s value %q", annotation, value)
	}
	return nil
}

func requireSize(annotation, value string) error {
	if !sizePattern.MatchString(value) {
		return fmt.Errorf("invalid %s value %q", annotation, value)
	}
	return nil
}

func resolveLatencyThreshold(annotations map[string]string, jfrSync string) (string, []string) {
	raw, present := annotationValue(annotations, LatencyThresholdAnnotation)
	if !present {
		return DefaultLatencyThreshold, nil
	}
	canonical, warning := canonicalLatency(raw)
	var warnings []string
	if warning != "" {
		warnings = append(warnings, warning)
	}
	if _, builtin := jfrSyncBuiltin[jfrSync]; jfrSync == "" || builtin {
		warnings = append(warnings, LatencyThresholdAnnotation+" has no effect unless the Jafra JFC preset is used")
	}
	return canonical, warnings
}

func canonicalLatency(raw string) (string, string) {
	matches := latencyPattern.FindStringSubmatch(raw)
	if matches == nil {
		return DefaultLatencyThreshold, latencyParseWarning(raw)
	}
	amount, err := strconv.ParseUint(matches[1], 10, 64)
	if err != nil {
		return DefaultLatencyThreshold, latencyParseWarning(raw)
	}
	multiplier := latencyUnitNanos[matches[2]]
	if multiplier == 0 || amount > ^uint64(0)/multiplier {
		return DefaultLatencyThreshold, latencyParseWarning(raw)
	}
	nanos := amount * multiplier
	const floorNanos = 100 * 1000 * 1000
	if nanos < floorNanos {
		return "100 ms", latencyFloorWarning(raw)
	}
	return formatLatency(nanos), ""
}

func latencyParseWarning(raw string) string {
	return fmt.Sprintf("%s value %q is not a JFR timespan; using 500 ms", LatencyThresholdAnnotation, raw)
}

func latencyFloorWarning(raw string) string {
	return fmt.Sprintf("%s value %q is below the 100 ms floor; using 100 ms", LatencyThresholdAnnotation, raw)
}

func formatLatency(nanos uint64) string {
	const (
		millisecond = 1000 * 1000
		microsecond = 1000
	)
	if nanos%millisecond == 0 {
		return fmt.Sprintf("%d ms", nanos/millisecond)
	}
	if nanos%microsecond == 0 {
		return fmt.Sprintf("%d us", nanos/microsecond)
	}
	return fmt.Sprintf("%d ns", nanos)
}
