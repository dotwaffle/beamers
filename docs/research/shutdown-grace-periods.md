# Shutdown grace periods

Surveyed 2026-07-21 against official Docker and Kubernetes documentation only.

## Defaults

| Environment | Default graceful-stop budget | Sequence |
| --- | ---: | --- |
| Docker Engine, Linux container | 10 seconds | Send the image/container stop signal, defaulting to `SIGTERM`; send `SIGKILL` if the process remains after the timeout. |
| Docker Engine, Windows container | 30 seconds | Same timeout behavior; the platform default differs. |
| Docker Compose | 10 seconds | Send `stop_signal`, defaulting to `SIGTERM`; send `SIGKILL` after `stop_grace_period`. |
| Kubernetes Pod | 30 seconds | Start the Pod grace-period clock, run `preStop` if configured, send the stop signal after the hook completes, then force remaining processes to stop when grace expires. |

Docker's `--stop-timeout` can set a per-container default, and `docker stop --timeout` can override the wait for a stop operation.
A value of `-1` makes Docker wait indefinitely.
The Engine defaults are 10 seconds for Linux containers and 30 seconds for Windows containers when the container has no configured default.
[Docker stop reference](https://docs.docker.com/reference/cli/docker/container/stop/)

Compose's `stop_grace_period` defaults to 10 seconds.
`stop_signal` defaults to `SIGTERM`; both can be set per service.
[Compose service reference](https://docs.docker.com/reference/compose-file/services/#stop_grace_period)

Kubernetes `spec.terminationGracePeriodSeconds` defaults to 30 seconds.
[Pod termination flow](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination-flow)

## Kubernetes sequencing

For a normal graceful Pod deletion:

1. The API records the deletion and grace period; the kubelet begins local
   shutdown when it observes the Pod as terminating.
2. If a container has `preStop` and the grace period is nonzero, kubelet runs the hook.
   The grace-period countdown has already started.
3. The hook must finish before kubelet asks the runtime to send the container's stop signal.
   This is normally `SIGTERM`, but a container image `STOPSIGNAL` or supported container-level override can change it.
4. At grace-period expiry, kubelet asks the runtime to `SIGKILL` remaining processes.
   If `preStop` is still running at expiry, kubelet requests one one-off two-second extension.

Consequently, `preStop` time and application shutdown time consume the **same** budget.
For example, a 20-second hook under the 30-second default leaves about 10 seconds for a Go process after it receives TERM, not another 30 seconds.
[Container lifecycle hooks](https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/#container-hooks)
[hook execution](https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/#hook-handler-execution)
[Pod termination flow](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination-flow)

Containers without native sidecar semantics receive stop requests in arbitrary order.
Kubernetes-managed sidecars are stopped after main containers and in reverse sidecar declaration order.
[Pod shutdown and sidecars](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-shutdown-and-sidecar-containers)

## Readiness and EndpointSlices during termination

Endpoint handling happens concurrently with kubelet shutdown; the documentation does not promise that traffic withdrawal completes before `preStop` or TERM.
The control plane does not immediately remove a terminating Pod's endpoint.
Instead, its EndpointSlice conditions become:

- `terminating: true`;
- `ready: false`, so ordinary load balancers stop selecting it for regular
  traffic; and
- `serving`: the endpoint's actual serving/readiness state, which draining-aware
  consumers can inspect.

For Pod-backed endpoints, the EndpointSlice controller derives `serving` from the Pod's `Ready` condition.
The official termination example therefore shows a Pod that is still `1/1` while terminating and an endpoint with `ready: false`, `serving: true`, and `terminating: true`.
[Pod termination flow](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination-flow)
[EndpointSlice conditions](https://kubernetes.io/docs/reference/kubernetes-api/discovery/endpoint-slice-v1/#EndpointConditions)
[termination example](https://kubernetes.io/docs/tutorials/services/pods-and-endpoint-termination-flow/)

A readiness probe is not required solely to withdraw a deleting Pod: Pod deletion itself sets the EndpointSlice endpoint's `ready` condition to false.
Readiness probes still run for the container's whole lifecycle and can withdraw an otherwise-running unhealthy Pod.
[Kubernetes probes](https://kubernetes.io/docs/concepts/workloads/pods/probes/#readiness-probe)

## Probe timing defaults

Kubernetes does not add probes automatically.
If a probe is configured and its timing fields are omitted, the common defaults are:

- `initialDelaySeconds`: 0;
- `periodSeconds`: 10;
- `timeoutSeconds`: 1;
- `successThreshold`: 1; and
- `failureThreshold`: 3.

While a container is not ready, readiness checks may run more often than `periodSeconds`.
Before its initial delay a configured readiness probe counts as failed; when no readiness probe exists, kubelet treats it as successful.
[Probe configuration fields](https://kubernetes.io/docs/concepts/workloads/pods/probes/#configuration-fields)

## Implications for a Go service

- Handle the effective stop signal in PID 1 and make the server stop accepting
  new work before waiting for in-flight work.
- Set an explicit platform grace period greater than `preStop` plus the
  application's worst-case drain/cleanup time, with margin for control-plane,
  kubelet, runtime, and network-draining delay.
- Do not rely on the Kubernetes 30-second default as 30 seconds of application
  cleanup when `preStop` is present.
- Align Docker Compose and Kubernetes settings explicitly; otherwise local
  Compose testing allows 10 seconds while an unset Kubernetes Pod allows 30.
