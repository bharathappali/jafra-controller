# Profile presets

`jafra.io/profile` selects `aggressive`, `medium`, or `low`. Unset means `medium`.

The webhook points `jfrsync` at `/jafra-agent/jafra-<profile>.jfc`. The init container copies those three files from `/opt/async-profiler/jfc/` in `quay.io/bharathappali/async-profiler:v4.5` onto the `jafra-profiler` emptyDir, next to `libasyncProfiler.so`. The application container mounts that emptyDir read-only. It does not mount the init image.

`jafra.io/jfrsync` set to `default` or `profile` is passed through to async-profiler in place of the Jafra file. `none`, `false`, and `off` omit `jfrsync`. Any other value is rejected. Per-knob annotations replace one preset field: `jafra.io/event`, `interval`, `wall`, `alloc`, `lock`, `nativemem` (including `off`, which drops both `nativemem` and `nofree`), and `memlimit`.

`loop=5s` is fixed on every preset, because one 5 s file is the analysis window. Every preset also sets `cstack=vm` and `nofree` when native memory sampling is on.

medium (default): `event=ctimer`, `interval=10ms`, `wall=100ms`, `alloc=1m`, `lock=1ms`, `nativemem=16m`, `jstackdepth=512`, `memlimit=32m`, `jfrsync=/jafra-agent/jafra-medium.jfc`.

low: `interval=20ms`, `wall=200ms`, `alloc=4m`, `lock=10ms`, `nativemem=64m`, `jstackdepth=512`, `memlimit=16m`, `jfrsync=/jafra-agent/jafra-low.jfc`.

aggressive: `interval=10ms`, `wall=50ms`, `alloc=512k`, `lock=100us`, `nativemem=2m`, `jstackdepth=1024`, `memlimit=64m`, `jfrsync=/jafra-agent/jafra-aggressive.jfc`.

`jafra.io/nativelock`, `jafra.io/chunktime`, `jafra.io/chunk-age`, `jafra.io/chunksize`, `jafra.io/live`, `jafra.io/alloc-live`, `jafra.io/loop`, and `jafra.io/nmt` are no longer supported and are ignored with a warning.

Native memory tracking is off by default. Set `jafra.io/jdk-nmt` to `"enabled"` to append `-XX:NativeMemoryTracking=summary`. The flag is injected at pod creation, because NMT cannot be enabled at attach time. Any other value leaves NMT off and adds a warning.

`jafra.io/latency-threshold` is a JFR timespan. The default is `500 ms` and the floor is `100 ms`. The init container rewrites only the `jdk.MethodTrace` threshold in the pod's copy of the JFC. JDK 25 records that event; JDK 17 and 21 ignore it. Changing the annotation requires a pod restart.
