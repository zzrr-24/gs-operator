# Extra Ingress Blue-Green Design

## Background

`GameService` currently switches traffic for the main game service by creating or
deleting the managed Ingress or HTTPRoute for the active deploy group. A new
`gamelogin` entry point must switch together with the same blue-green rollout.

The new entry point serves a different backend service set. Each backend has a
blue and green version, and the Ingress backend Service name must change with
`spec.deployGroup.role`.

## Goals

- Add an optional `spec.extraIngress` block to `GameService`.
- Reuse `spec.route.host` as the host for the extra Ingress.
- Reuse `spec.connectorNamespace` as the namespace for the extra Ingress.
- Generate each backend Service name as
  `{serviceBaseName}-{spec.deployGroup.role}`.
- Reconcile the extra Ingress for both `trafficMode: Ingress` and
  `trafficMode: Gateway`.
- Keep existing behavior unchanged when `spec.extraIngress` is not configured.

## Non-Goals

- Do not add a new CRD.
- Do not support multiple extra Ingress objects in this change.
- Do not manage the backend Services for the extra Ingress. They are expected to
  exist outside this controller.
- Do not support a separate host for the extra Ingress.

## API Shape

Add new API types under `api/v1alpha1/gameservice_types.go`:

```go
type ExtraIngressConfig struct {
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    Name string `json:"name"`

    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    IngressClassName string `json:"ingressClassName"`

    TLS *TLSConfig `json:"tls,omitempty"`
    Annotations map[string]string `json:"annotations,omitempty"`

    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinItems=1
    Paths []ExtraIngressPath `json:"paths"`
}

type ExtraIngressPath struct {
    // +kubebuilder:validation:Enum=Prefix;Exact;ImplementationSpecific
    PathType string `json:"pathType"`

    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    Path string `json:"path"`

    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    ServiceBaseName string `json:"serviceBaseName"`

    // +kubebuilder:validation:Required
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=65535
    Port int32 `json:"port"`
}
```

Add the optional field:

```go
ExtraIngress *ExtraIngressConfig `json:"extraIngress,omitempty"`
```

Example:

```yaml
spec:
  route:
    host: adventure.zzrr.io
    pathType: Prefix
    pathPrefix: /connector
    port: 3010
  extraIngress:
    name: gamelogin
    ingressClassName: higress
    paths:
      - pathType: Prefix
        path: /serverlogin
        serviceBaseName: logingame
        port: 9020
      - pathType: Prefix
        path: /servergovern
        serviceBaseName: servergovern
        port: 9015
      - pathType: Prefix
        path: /backend
        serviceBaseName: backend
        port: 9010
      - pathType: Prefix
        path: /statsgm
        serviceBaseName: statsgm
        port: 9012
```

For `deployGroup.role: blue`, the generated backends are:

- `/serverlogin` -> `logingame-blue:9020`
- `/servergovern` -> `servergovern-blue:9015`
- `/backend` -> `backend-blue:9010`
- `/statsgm` -> `statsgm-blue:9012`

## Controller Behavior

The existing `IngressManager` will gain extra Ingress methods:

- `ReconcileExtraIngress(ctx, gs)`
- `DeleteExtraIngress(ctx, gs)`

When `spec.extraIngress` is absent, both methods are no-ops.

When `spec.deployGroup.active` is true:

1. Reconcile existing main traffic entry as today.
2. Create or update `Ingress/<spec.connectorNamespace>/<spec.extraIngress.name>`.
3. Set `spec.ingressClassName` from `spec.extraIngress.ingressClassName`.
4. Set the only rule host to `spec.route.host`.
5. Build each path from `spec.extraIngress.paths`.
6. Set backend Service name to
   `{path.serviceBaseName}-{spec.deployGroup.role}`.
7. Copy configured annotations and labels.
8. Set TLS with `spec.route.host` when `spec.extraIngress.tls` is configured.

When `spec.deployGroup.active` is false:

1. Delete the main Ingress or HTTPRoute as today.
2. Delete the configured extra Ingress only when its `gs-role` label still
   matches this `GameService` role.

During finalization:

1. Delete the main Ingress and HTTPRoute as today.
2. Delete the configured extra Ingress if it exists.
3. Continue existing Service cleanup and finalizer removal.

This follows the existing blue-green deletion semantics. If the old active
`GameService` reconciles as standby before the new active `GameService`
reconciles, the extra Ingress can be temporarily deleted. This is accepted
because it matches the current main traffic behavior.

Because the extra Ingress uses one configured name instead of role-specific
names like `game-ingress-blue` and `game-ingress-green`, deletion must be guarded
by the `gs-role` label. This prevents a standby `GameService` from deleting an
extra Ingress that has already been recreated or updated by the current active
`GameService`.

## Labels And Ownership

The extra Ingress should use labels that distinguish it from the existing main
Ingress:

```yaml
app.kubernetes.io/managed-by: gs-operator
gs-role: <deployGroup.role>
gs-extra-ingress: "true"
```

Set an owner reference only when the `GameService` namespace matches
`spec.connectorNamespace`, following the existing Ingress behavior. Cross
namespace management relies on labels because Kubernetes disallows cross
namespace owner references.

## Error Handling And Status

If extra Ingress reconciliation fails for an active group:

- emit a warning event with reason `ExtraIngressReconcileFailed`
- set `Available=False` with reason `ExtraIngressReconcileFailed`
- return the error so reconciliation retries

If extra Ingress deletion fails for a standby group:

- emit a warning event with reason `ExtraIngressDeleteFailed`
- set `Available=False` with reason `ExtraIngressDeleteFailed`
- continue matching current standby cleanup behavior where possible

If the extra Ingress succeeds, existing `Available=True` reasons for the main
traffic mode can remain unchanged. The condition message may mention the extra
Ingress only if that can be done without broad status churn.

## Testing

Add focused controller tests:

- active `GameService` creates the extra Ingress with host from
  `spec.route.host`
- active `GameService` generates backend Service names with
  `{serviceBaseName}-{deployGroup.role}`
- `trafficMode: Gateway` still reconciles the extra Ingress
- standby `GameService` deletes the extra Ingress
- missing `spec.extraIngress` preserves current behavior

Run:

```bash
make manifests
make generate
make test
```

## Open Decisions

All design decisions needed for implementation are resolved:

- one extra Ingress object per `GameService`
- Ingress name is configured in `spec.extraIngress.name`
- namespace is always `spec.connectorNamespace`
- host is always `spec.route.host`
- Service name format is `{serviceBaseName}-{deployGroup.role}`
- standby deletes the extra Ingress only when the existing object's `gs-role`
  label matches the standby role
