# Jafra Controller

`jafra-controller` version `0.0.1` is a Go mutating admission webhook. It
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
    jafra.io/event: "ctimer"
    jafra.io/interval: "20ms"
    jafra.io/wall: "100ms"
    jafra.io/alloc: "1m"
    jafra.io/live: "true"
    jafra.io/lock: "10ms"
    jafra.io/nativemem: "2m"
    jafra.io/nativelock: "10ms"
    jafra.io/memlimit: "128m"
    jafra.io/loop: "1m"
    jafra.io/chunktime: "5s"
    jafra.io/chunksize: "32m"
    jafra.io/jfrsync: "default"
```

The target list is comma-separated and every named container must exist. The
controller skips unlabelled Pods, Pods without `continuous` mode, previously
injected Pods, and Pods in `kube-system` or `jafra-system`. It rejects invalid
modes, empty target lists, and unknown target containers.

Profiler annotations are optional. Production-oriented defaults are
`event=ctimer`, `interval=20ms`, `wall=100ms`, `alloc=1m`, `live=true`,
`lock=10ms`, `nativemem=2m`, `nativelock=10ms`, `memlimit=128m`, `loop=5m`,
`chunktime=5s`, `chunksize=32m`, and `jfrsync=default`. `ctimer` is used
instead of Linux `perf_event_open` so application Pods do not need extra
privileges. `jfrsync=default` starts JDK Flight Recorder beside the
profiler so GC pauses, compilation, and I/O land in the same JFR file.
Set `jafra.io/jfrsync: "none"` to turn that off. A `chunktime` below five
seconds is rejected because async-profiler cannot honor it. `proc`
host-process sampling is not injected.

Successful mutation adds:

```yaml
metadata:
  annotations:
    jafra.io/injected: "true"
    jafra.io/injected-version: "0.0.1"
```

The mutation also adds:

- An `emptyDir` containing only `/jafra-agent/libasyncProfiler.so`.
- A node-local `hostPath` rooted at `/var/lib/jafra/recordings`.
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
docker build -t quay.io/bharathappali/jafra-controller:0.0.1 .
```

Push the image or load it into the demonstration cluster before deployment.
If a different registry is used, update `deploy/controller/deployment.yaml`.

## Deploy

The cluster must already have cert-manager installed. Deploy in this order so
the webhook is not registered before its TLS endpoint is ready:

```bash
kubectl apply -f deploy/controller/namespace.yaml
kubectl apply -f deploy/controller/rbac.yaml
kubectl apply -f deploy/controller/certificate.yaml
kubectl wait --for=condition=Ready certificate/jafra-controller-serving-cert \
  -n jafra-system --timeout=120s
kubectl apply -f deploy/controller/service.yaml
kubectl apply -f deploy/controller/deployment.yaml
kubectl rollout status deployment/jafra-controller \
  -n jafra-system --timeout=120s
kubectl apply -f deploy/controller/webhook.yaml
```

Run these commands from the repository root; build commands run from
`jafra-controller/`.

## Checkpoint 1 demonstration

```bash
kubectl apply -f deploy/examples/checkpoint-1-pods.yaml
kubectl get pod plain-pod -o yaml
kubectl get pod profiled-pod -o yaml
```

`plain-pod` must have no Jafra injection annotation. `profiled-pod` must have
both injection annotations and report version `0.0.1`.

## Checkpoint 2 demonstration

Rebuild and load or push the `0.0.1` image, restart the controller, then run:

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
`event=ctimer,interval=20ms,wall=100ms,alloc=1m,live,lock=10ms,nativemem=2m,nativelock=10ms,memlimit=128m,jfrsync=default`.
The example container requests `768Mi` and limits `1Gi` so heap plus native
and JFR overhead stay under the cgroup limit. `proc` sampling is rejected.

## Intentional limitations

- TLS issuance requires cert-manager.
- The single replica is sufficient for the demonstration, not high
  availability.
- The controller intentionally fails admission for opted-in Pods with invalid
  configuration.
- The node-local demonstration requires `hostPath`, so a namespace enforcing
  the Restricted Pod Security Standard can reject profiled Pods.
- Recording leaf directories use mode `0777` because application UID
  discovery is not implemented. This is demonstration-only technical debt.
- The selected container mount relies on kubelet accepting an init-created
  `subPathExpr`. If the target cluster rejects it, mount the recording root
  temporarily and document the broader filesystem exposure; do not silently
  change this architecture.
