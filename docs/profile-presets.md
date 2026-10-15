# Profile presets

`jafra.io/profile` selects `aggressive`, `medium`, or `low`. Unset means `medium`.

The webhook points `jfrsync` at `/jafra-agent/jafra-<profile>.jfc`. The init container copies those three files from `/opt/async-profiler/jfc/` in `quay.io/bharathappali/async-profiler:v4.5` onto the `jafra-profiler` emptyDir, next to `libasyncProfiler.so`. The application container mounts that emptyDir read-only. It does not mount the init image.

`jafra.io/jfrsync` set to `default` or `profile` is passed through to async-profiler in place of the Jafra file. `none`, `false`, and `off` omit `jfrsync`. Any other value is rejected. Per-knob annotations replace one preset field. `jafra.io/chunk-age` and `jafra.io/chunktime` are the same setting and must agree. The floor and the default are 5s on every preset: async-profiler holds the open chunk in memory until `chunktime` or `chunksize`, and the agent ships a chunk only after its JFR header is finalized. A longer chunk age is an explicit opt-in and slows that stream.

`jafra.io/nmt` defaults to `summary` and appends `-XX:NativeMemoryTracking=summary` so `jdk.NativeMemoryUsage` can be recorded. `off` adds nothing. `jafra.io/alloc-live` defaults to false.
