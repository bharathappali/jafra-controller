# Jafra Controller

`jafra-controller` version `0.0.2` is a Go mutating admission webhook. It
validates the Jafra Pod opt-in API and injects async-profiler into explicitly
selected Java containers.

## Pod opt-in

An eligible Pod must provide:

```yaml
metadata:
  labels:
    jafra.io/enabled: "true"
    jafra.io/mode: "continuous"
  annotations:
    jafra.io/containers: "application-container"
    jafra.io/profile: "medium"
```

The target list is comma-separated and every named container must exist. The
controller skips unlabelled Pods, Pods without `continuous` mode, previously
injected Pods, and Pods in `kube-system` or `jafra-system`. It rejects invalid
modes, empty target lists, and unknown target containers.

`jafra.io/profile` selects `aggressive`, `medium`, or `low`. The default is
`medium`: `event=ctimer`, `interval=10ms`, `wall=100ms`, `alloc=1m`,
`lock=1ms`, `nativemem=16m`, `nofree`, `cstack=vm`, `jstackdepth=512`,
`memlimit=32m`, `loop=5s`, and
`jfrsync=/jafra-agent/jafra-medium.jfc`. `loop=5s` is fixed, because one
5 s file is the analysis window.
`ctimer` is used instead of Linux `perf_event_open` so application Pods do
not need extra privileges. The init container copies `jafra-aggressive.jfc`,
`jafra-medium.jfc`, and `jafra-low.jfc` next to `libasyncProfiler.so`, and
the JVM loads the file for the selected profile. Set `jafra.io/jfrsync` to
`default` or `profile` to pass that JDK configuration name to async-profiler
instead of a Jafra file, or `none` to record profiler samples only. Custom
paths are rejected. `jafra.io/nativelock`, `jafra.io/chunktime`,
`jafra.io/chunk-age`, `jafra.io/chunksize`, `jafra.io/live`,
`jafra.io/alloc-live`, `jafra.io/loop`, and `jafra.io/nmt` are no longer
supported and are ignored with a warning.
Native memory tracking is off by default. Set `jafra.io/jdk-nmt` to
`"enabled"` to append `-XX:NativeMemoryTracking=summary` unless that flag is
already present. The flag is injected at pod creation, because NMT cannot be
enabled at attach time. `jafra.io/latency-threshold` is a JFR timespan, default
`500 ms`, floor `100 ms`. The init container rewrites only the `jdk.MethodTrace`
threshold in the pod's copy of the JFC. JDK 25 records that event; JDK 17 and
21 ignore it. Changing the annotation requires a pod restart. An explicit per-knob
annotation replaces that one preset field. `proc` host-process sampling is
not injected.

Successful mutation adds:

```yaml
metadata:
  annotations:
    jafra.io/injected: "true"
    jafra.io/injected-version: "0.0.2"
```

The mutation also adds:

- An `emptyDir` containing `/jafra-agent/libasyncProfiler.so` and the three
  `jafra-*.jfc` presets.
- An `emptyDir` named `jafra-recordings` for JFR output.
- An init container using
  `quay.io/bharathappali/async-profiler:v4.5`.
- A per-container `/jfr-data` mount using the namespace, Pod UID, and
  container name as its `subPathExpr`.
- Downward API identity variables and a literal container-name variable.
- An async-profiler `-agentpath` appended to an existing literal
  `JAVA_TOOL_OPTIONS`.

`JAVA_TOOL_OPTIONS` backed by `valueFrom` is rejected because the webhook
cannot safely evaluate and merge it.

## Test and build

```bash
go test ./...
go build ./cmd/controller
docker build -t quay.io/bharathappali/jafra-controller:0.0.2 .
```

Push the image or load it into the demonstration cluster before deployment.

## Deploy

From the **jafra-io** repository root, use `./install-jafra.sh` or `./pull-jafra.sh`
(Kind) or `./install-jafra.sh --target openshift` (OpenShift). Set
`JAFRA_REGISTRY` and `JAFRA_VERSION` before install; Kustomize applies the tag
from `deploy/kustomize/`.

Manual phased apply (cert-manager required; run from jafra-io root):

```bash
export JAFRA_VERSION=0.0.2   # optional override
./install-jafra.sh --deploy-only   # or apply phases via scripts/lib/deploy-jafra.sh
```

Cluster-scoped webhook: `kubectl apply -f deploy/controller/webhook.yaml` (after
the controller Service is up).

Build commands run from `jafra-controller/`.

## Checkpoint 1 demonstration

```bash
kubectl apply -f deploy/examples/checkpoint-1-pods.yaml
kubectl get pod plain-pod -o yaml
kubectl get pod profiled-pod -o yaml
```

`plain-pod` must have no Jafra injection annotation. `profiled-pod` must have
both injection annotations and report version `0.0.2`.

## Checkpoint 2 demonstration

Rebuild and load or push the `0.0.2` image, restart the controller, then run:

```bash
kubectl apply -f deploy/examples/auth-cache.yaml
kubectl rollout status deployment/auth-cache --timeout=120s
kubectl get pod -l app.kubernetes.io/name=auth-cache -o yaml
kubectl exec deployment/auth-cache -c auth-cache -- ls -lah /jfr-data
```

After approximately one minute, both `profile-0.jfr` and `profile-1.jfr`
should exist. Copy a closed file and validate it with a local JDK:

```bash
pod="$(kubectl get pod -l app.kubernetes.io/name=auth-cache \
  -o jsonpath='{.items[0].metadata.name}')"
kubectl cp "${pod}:/jfr-data/profile-0.jfr" ./profile-0.jfr -c auth-cache
jfr summary ./profile-0.jfr
```

Metrics are served on port `8080`. Health and readiness checks are served on
port `8081`.

The bounded profiler string injected for `auth-cache` includes
`event=ctimer,interval=10ms,wall=100ms,alloc=1m,lock=1ms,nativemem=16m,nofree,cstack=vm,jstackdepth=512,memlimit=32m,loop=5s,jfrsync=/jafra-agent/jafra-medium.jfc`.
`-XX:NativeMemoryTracking=summary` is added only when `jafra.io/jdk-nmt` is `"enabled"`.
The example container requests `768Mi` and limits `1Gi` so heap plus native
and JFR overhead stay under the cgroup limit. `proc` sampling is rejected.

## Intentional limitations

- TLS issuance requires cert-manager.
- The single replica is sufficient for the demonstration, not high
  availability.
- The controller intentionally fails admission for opted-in Pods with invalid
  configuration.
- Opted-in Pods use `emptyDir` recordings only. The `jafra-agent` DaemonSet
  reads those files from the kubelet pod volume path on each node.
- The selected container mount relies on kubelet accepting an init-created
  `subPathExpr`. If the target cluster rejects it, mount the recording root
  temporarily and document the broader filesystem exposure; do not silently
  change this architecture.
