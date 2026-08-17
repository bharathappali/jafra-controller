package annotations

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	EnabledLabel              = "jafra.io/enabled"
	ModeLabel                 = "jafra.io/mode"
	ContainersAnnotation      = "jafra.io/containers"
	EventAnnotation           = "jafra.io/event"
	IntervalAnnotation        = "jafra.io/interval"
	WallAnnotation            = "jafra.io/wall"
	AllocAnnotation           = "jafra.io/alloc"
	LiveAnnotation            = "jafra.io/live"
	LockAnnotation            = "jafra.io/lock"
	NativeMemAnnotation       = "jafra.io/nativemem"
	NativeLockAnnotation      = "jafra.io/nativelock"
	MemLimitAnnotation        = "jafra.io/memlimit"
	LoopAnnotation            = "jafra.io/loop"
	ChunkTimeAnnotation       = "jafra.io/chunktime"
	ChunkSizeAnnotation       = "jafra.io/chunksize"
	JfrSyncAnnotation         = "jafra.io/jfrsync"
	InjectedAnnotation        = "jafra.io/injected"
	InjectedVersionAnnotation = "jafra.io/injected-version"

	EnabledValue   = "true"
	ContinuousMode = "continuous"

	DefaultEvent      = "ctimer"
	DefaultInterval   = "20ms"
	DefaultWall       = "100ms"
	DefaultAlloc      = "1m"
	DefaultLive       = "true"
	DefaultLock       = "10ms"
	DefaultNativeMem  = "2m"
	DefaultNativeLock = "10ms"
	DefaultMemLimit   = "128m"
	DefaultLoop       = "5m"
	DefaultChunkTime  = "5s"
	DefaultChunkSize  = "32m"
	DefaultJfrSync    = "default"
)

var (
	eventPattern    = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	durationPattern = regexp.MustCompile(`^([1-9][0-9]*)(ns|us|ms|s|m|h|d)$`)
	sizePattern     = regexp.MustCompile(`^[1-9][0-9]*[kmg]$`)
	allowedEvents   = map[string]struct{}{
		"ctimer": {},
		"cpu":    {},
		"wall":   {},
		"itimer": {},
	}
	allowedJfrSync = map[string]struct{}{
		"default": {},
		"profile": {},
		"none":    {},
		"false":   {},
		"off":     {},
	}
)

type Selection struct {
	Eligible   bool
	Containers []string
}

type ProfilerConfig struct {
	Event      string
	Interval   string
	Wall       string
	Alloc      string
	Live       bool
	Lock       string
	NativeMem  string
	NativeLock string
	MemLimit   string
	Loop       string
	ChunkTime  string
	ChunkSize  string
	JfrSync    string
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

// ResolveProfilerConfig applies defaults and rejects values that cannot be
// represented safely in an async-profiler agent option.
func ResolveProfilerConfig(pod *corev1.Pod) (ProfilerConfig, error) {
	liveValue := strings.ToLower(valueOrDefault(pod.Annotations, LiveAnnotation, DefaultLive))
	live, err := parseBool(liveValue, LiveAnnotation)
	if err != nil {
		return ProfilerConfig{}, err
	}

	profile := ProfilerConfig{
		Event:      valueOrDefault(pod.Annotations, EventAnnotation, DefaultEvent),
		Interval:   valueOrDefault(pod.Annotations, IntervalAnnotation, DefaultInterval),
		Wall:       valueOrDefault(pod.Annotations, WallAnnotation, DefaultWall),
		Alloc:      valueOrDefault(pod.Annotations, AllocAnnotation, DefaultAlloc),
		Live:       live,
		Lock:       valueOrDefault(pod.Annotations, LockAnnotation, DefaultLock),
		NativeMem:  valueOrDefault(pod.Annotations, NativeMemAnnotation, DefaultNativeMem),
		NativeLock: valueOrDefault(pod.Annotations, NativeLockAnnotation, DefaultNativeLock),
		MemLimit:   valueOrDefault(pod.Annotations, MemLimitAnnotation, DefaultMemLimit),
		Loop:       valueOrDefault(pod.Annotations, LoopAnnotation, DefaultLoop),
		ChunkTime:  valueOrDefault(pod.Annotations, ChunkTimeAnnotation, DefaultChunkTime),
		ChunkSize:  valueOrDefault(pod.Annotations, ChunkSizeAnnotation, DefaultChunkSize),
		JfrSync:    strings.ToLower(valueOrDefault(pod.Annotations, JfrSyncAnnotation, DefaultJfrSync)),
	}

	if len(profile.Event) > 64 || !eventPattern.MatchString(profile.Event) {
		return ProfilerConfig{}, fmt.Errorf("invalid %s value %q", EventAnnotation, profile.Event)
	}
	if _, allowed := allowedEvents[profile.Event]; !allowed {
		return ProfilerConfig{}, fmt.Errorf("unsupported %s value %q", EventAnnotation, profile.Event)
	}
	if err := requireDuration(IntervalAnnotation, profile.Interval); err != nil {
		return ProfilerConfig{}, err
	}
	if err := requireDuration(WallAnnotation, profile.Wall); err != nil {
		return ProfilerConfig{}, err
	}
	if err := requireDuration(LockAnnotation, profile.Lock); err != nil {
		return ProfilerConfig{}, err
	}
	if err := requireDuration(NativeLockAnnotation, profile.NativeLock); err != nil {
		return ProfilerConfig{}, err
	}
	if err := requireSize(AllocAnnotation, profile.Alloc); err != nil {
		return ProfilerConfig{}, err
	}
	if err := requireSize(NativeMemAnnotation, profile.NativeMem); err != nil {
		return ProfilerConfig{}, err
	}
	if err := requireSize(MemLimitAnnotation, profile.MemLimit); err != nil {
		return ProfilerConfig{}, err
	}
	if _, err := durationSeconds(profile.Loop); err != nil {
		return ProfilerConfig{}, fmt.Errorf("invalid %s value %q: %w", LoopAnnotation, profile.Loop, err)
	}
	chunkSeconds, err := durationSeconds(profile.ChunkTime)
	if err != nil {
		return ProfilerConfig{}, fmt.Errorf("invalid %s value %q: %w", ChunkTimeAnnotation, profile.ChunkTime, err)
	}
	if chunkSeconds < 5 {
		return ProfilerConfig{}, fmt.Errorf("%s must be at least 5s", ChunkTimeAnnotation)
	}
	if err := requireSize(ChunkSizeAnnotation, profile.ChunkSize); err != nil {
		return ProfilerConfig{}, err
	}
	if _, allowed := allowedJfrSync[profile.JfrSync]; !allowed {
		return ProfilerConfig{}, fmt.Errorf("unsupported %s value %q", JfrSyncAnnotation, profile.JfrSync)
	}
	if profile.JfrSync == "none" || profile.JfrSync == "false" || profile.JfrSync == "off" {
		profile.JfrSync = ""
	}

	return profile, nil
}

func (c ProfilerConfig) AgentOption(libraryPath string) string {
	parts := []string{
		"start",
		"event=" + c.Event,
		"interval=" + c.Interval,
		"wall=" + c.Wall,
		"alloc=" + c.Alloc,
	}
	if c.Live {
		parts = append(parts, "live")
	}
	parts = append(parts,
		"lock="+c.Lock,
		"nativemem="+c.NativeMem,
		"nativelock="+c.NativeLock,
		"memlimit="+c.MemLimit,
		"loop="+c.Loop,
		"chunktime="+c.ChunkTime,
		"chunksize="+c.ChunkSize,
	)
	if c.JfrSync != "" {
		parts = append(parts, "jfrsync="+c.JfrSync)
	}
	parts = append(parts, "file=/jfr-data/profile-%n.jfr")
	return "-agentpath:" + libraryPath + "=" + strings.Join(parts, ",")
}

func valueOrDefault(values map[string]string, key, defaultValue string) string {
	if value, exists := values[key]; exists {
		return strings.TrimSpace(value)
	}
	return defaultValue
}

func parseBool(value, annotation string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s value %q", annotation, value)
	}
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

func durationSeconds(value string) (uint64, error) {
	matches := durationPattern.FindStringSubmatch(value)
	if matches == nil {
		return 0, fmt.Errorf("expected a positive integer followed by ns, us, ms, s, m, h, or d")
	}
	amount, err := strconv.ParseUint(matches[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("duration is too large")
	}
	multiplier := map[string]uint64{
		"ns": 0,
		"us": 0,
		"ms": 0,
		"s":  1,
		"m":  60,
		"h":  60 * 60,
		"d":  24 * 60 * 60,
	}[matches[2]]
	if multiplier == 0 {
		return 0, fmt.Errorf("duration must be at least one second")
	}
	if amount > ^uint64(0)/multiplier {
		return 0, fmt.Errorf("duration is too large")
	}
	return amount * multiplier, nil
}
