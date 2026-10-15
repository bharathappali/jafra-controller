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
	ChunkAgeAnnotation        = "jafra.io/chunk-age"
	ChunkSizeAnnotation       = "jafra.io/chunksize"
	JfrSyncAnnotation         = "jafra.io/jfrsync"
	ProfileAnnotation         = "jafra.io/profile"
	NmtAnnotation             = "jafra.io/nmt"
	AllocLiveAnnotation       = "jafra.io/alloc-live"
	InjectedAnnotation        = "jafra.io/injected"
	InjectedVersionAnnotation = "jafra.io/injected-version"

	EnabledValue   = "true"
	ContinuousMode = "continuous"

	DefaultEvent      = "ctimer"
	DefaultInterval   = "20ms"
	DefaultWall       = "100ms"
	DefaultAlloc      = "1m"
	DefaultLock       = "5ms"
	DefaultNativeMem  = "2m"
	DefaultNativeLock = "5ms"
	DefaultMemLimit   = "128m"
	DefaultLoop       = "10m"
	DefaultChunkTime  = "5s"
	DefaultChunkSize  = "32m"
	DefaultProfile    = "medium"
	DefaultNmt        = "summary"

	jfcDirectory         = "/jafra-agent"
	minChunkSeconds      = 5
	nativeMemoryTracking = "-XX:NativeMemoryTracking=summary"
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
	allowedNmt = map[string]struct{}{
		"summary": {},
		"off":     {},
	}
	presetKnobs = map[string]ProfilerConfig{
		"aggressive": {
			Event: "ctimer", Interval: "10ms", Wall: "50ms", Alloc: "512k",
			Lock: "1ms", NativeMem: "1m", NativeLock: "1ms",
			MemLimit: "128m", Loop: "10m", ChunkTime: "5s", ChunkSize: "32m",
		},
		"medium": {
			Event: "ctimer", Interval: "20ms", Wall: "100ms", Alloc: "1m",
			Lock: "5ms", NativeMem: "2m", NativeLock: "5ms",
			MemLimit: "128m", Loop: "10m", ChunkTime: "5s", ChunkSize: "32m",
		},
		"low": {
			Event: "ctimer", Interval: "50ms", Wall: "200ms", Alloc: "4m",
			Lock: "20ms", NativeLock: "20ms",
			MemLimit: "128m", Loop: "15m", ChunkTime: "5s", ChunkSize: "32m",
		},
	}
)

type Selection struct {
	Eligible   bool
	Containers []string
}

type ProfilerConfig struct {
	Profile    string
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
	Nmt        string
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
// per-knob annotations. Chunk time stays at 5s unless the pod opts into a
// longer interval, because async-profiler holds the open chunk in memory
// until chunktime elapses.
func ResolveProfilerConfig(pod *corev1.Pod) (ProfilerConfig, error) {
	profileName := strings.ToLower(valueOrDefault(pod.Annotations, ProfileAnnotation, DefaultProfile))
	if _, allowed := allowedProfiles[profileName]; !allowed {
		return ProfilerConfig{}, fmt.Errorf("unsupported %s value %q", ProfileAnnotation, profileName)
	}
	preset := presetKnobs[profileName]
	annotations := pod.Annotations

	profile := ProfilerConfig{
		Profile:    profileName,
		Event:      overrideOr(annotations, EventAnnotation, preset.Event),
		Interval:   overrideOr(annotations, IntervalAnnotation, preset.Interval),
		Wall:       overrideOr(annotations, WallAnnotation, preset.Wall),
		Alloc:      overrideOr(annotations, AllocAnnotation, preset.Alloc),
		Lock:       overrideOr(annotations, LockAnnotation, preset.Lock),
		NativeMem:  overrideOr(annotations, NativeMemAnnotation, preset.NativeMem),
		NativeLock: overrideOr(annotations, NativeLockAnnotation, preset.NativeLock),
		MemLimit:   overrideOr(annotations, MemLimitAnnotation, preset.MemLimit),
		Loop:       overrideOr(annotations, LoopAnnotation, preset.Loop),
		ChunkSize:  overrideOr(annotations, ChunkSizeAnnotation, preset.ChunkSize),
		Nmt:        strings.ToLower(valueOrDefault(annotations, NmtAnnotation, DefaultNmt)),
	}

	chunkTime, chunkAnnotation, err := resolveChunkTime(annotations, preset.ChunkTime)
	if err != nil {
		return ProfilerConfig{}, err
	}
	profile.ChunkTime = chunkTime

	live, err := resolveLive(annotations)
	if err != nil {
		return ProfilerConfig{}, err
	}
	profile.Live = live

	jfrSync, err := resolveJfrSync(annotations, profileName)
	if err != nil {
		return ProfilerConfig{}, err
	}
	profile.JfrSync = jfrSync

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
	if profile.NativeLock != "" {
		if err := requireDuration(NativeLockAnnotation, profile.NativeLock); err != nil {
			return ProfilerConfig{}, err
		}
	}
	if err := requireSize(AllocAnnotation, profile.Alloc); err != nil {
		return ProfilerConfig{}, err
	}
	if strings.EqualFold(profile.NativeMem, "off") {
		profile.NativeMem = ""
	} else if profile.NativeMem != "" {
		if err := requireSize(NativeMemAnnotation, profile.NativeMem); err != nil {
			return ProfilerConfig{}, err
		}
	}
	if err := requireSize(MemLimitAnnotation, profile.MemLimit); err != nil {
		return ProfilerConfig{}, err
	}
	if _, err := durationSeconds(profile.Loop); err != nil {
		return ProfilerConfig{}, fmt.Errorf("invalid %s value %q: %w", LoopAnnotation, profile.Loop, err)
	}
	chunkSeconds, err := durationSeconds(profile.ChunkTime)
	if err != nil {
		return ProfilerConfig{}, fmt.Errorf("invalid %s value %q: %w", chunkAnnotation, profile.ChunkTime, err)
	}
	if chunkSeconds < minChunkSeconds {
		return ProfilerConfig{}, fmt.Errorf("%s must be at least 5s", chunkAnnotation)
	}
	if err := requireSize(ChunkSizeAnnotation, profile.ChunkSize); err != nil {
		return ProfilerConfig{}, err
	}
	if _, allowed := allowedNmt[profile.Nmt]; !allowed {
		return ProfilerConfig{}, fmt.Errorf("unsupported %s value %q", NmtAnnotation, profile.Nmt)
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
	parts = append(parts, "lock="+c.Lock)
	if c.NativeMem != "" {
		parts = append(parts, "nativemem="+c.NativeMem)
	}
	if c.NativeLock != "" {
		parts = append(parts, "nativelock="+c.NativeLock)
	}
	parts = append(parts,
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

// NativeMemoryOption is the JVM flag for jafra.io/nmt=summary.
// An empty string means the webhook must not inject the flag.
func (c ProfilerConfig) NativeMemoryOption() string {
	if c.Nmt == "summary" {
		return nativeMemoryTracking
	}
	return ""
}

func resolveChunkTime(annotations map[string]string, preset string) (string, string, error) {
	age, hasAge := annotationValue(annotations, ChunkAgeAnnotation)
	chunk, hasChunk := annotationValue(annotations, ChunkTimeAnnotation)
	switch {
	case hasAge && hasChunk && age != chunk:
		return "", "", fmt.Errorf("%s and %s disagree: %q vs %q", ChunkAgeAnnotation, ChunkTimeAnnotation, age, chunk)
	case hasChunk:
		return chunk, ChunkTimeAnnotation, nil
	case hasAge:
		return age, ChunkAgeAnnotation, nil
	default:
		return preset, ChunkTimeAnnotation, nil
	}
}

func resolveLive(annotations map[string]string) (bool, error) {
	allocRaw, hasAlloc := annotationValue(annotations, AllocLiveAnnotation)
	liveRaw, hasLive := annotationValue(annotations, LiveAnnotation)
	if !hasAlloc && !hasLive {
		return false, nil
	}
	var allocLive bool
	var err error
	if hasAlloc {
		allocLive, err = parseBool(strings.ToLower(allocRaw), AllocLiveAnnotation)
		if err != nil {
			return false, err
		}
	}
	var live bool
	if hasLive {
		live, err = parseBool(strings.ToLower(liveRaw), LiveAnnotation)
		if err != nil {
			return false, err
		}
	}
	if hasAlloc && hasLive && allocLive != live {
		return false, fmt.Errorf("%s and %s disagree", AllocLiveAnnotation, LiveAnnotation)
	}
	if hasLive {
		return live, nil
	}
	return allocLive, nil
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
