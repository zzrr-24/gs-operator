# 附加入口蓝绿切换设计

## 背景

`GameService` 目前会根据部署组的 active 状态，为主游戏服务创建或删除受管的
Ingress 或 HTTPRoute，从而完成蓝绿流量切换。

现在需要新增一个 `gamelogin` 附加入口。它属于另一套后端服务，但必须跟随同一个
`GameService` 蓝绿切换一起切换。该后端也有蓝、绿两套代码版本，不过后端
Service 已由外部系统创建，operator 只需要在附加入口中引用可直接调用的
Service。

## 目标

- 在 `GameService` 中新增可选字段 `spec.extraIngress`，用于描述一个随蓝绿切换同步更新的附加入口。
- 附加入口的 host 固定复用 `spec.route.host`。
- 附加入口的 namespace 固定复用 `spec.connectorNamespace`。
- 附加入口根据 `spec.trafficMode` 区分资源类型：
  - `Ingress` 模式创建或更新 Ingress。
  - `Gateway` 模式创建或更新 HTTPRoute。
- `Ingress` 模式下，附加 Ingress 的 `ingressClassName` 和 TLS 复用 `spec.ingress` 中的配置。
- 附加入口 annotations 可以与主入口差异化，由 `spec.extraIngress.annotations` 单独配置。
- 附加入口资源名根据 `spec.extraIngress.name`、`spec.trafficMode` 和
  `spec.deployGroup.role` 生成，切换方式与现有 connector 主入口一致。
- 后端 Service 名称直接使用配置中的 `serviceName`，operator 不生成、不拼接、不创建这些 Service。
- 未配置 `spec.extraIngress` 时保持现有行为不变。

## 非目标

- 不新增 CRD。
- 本次只支持一个附加入口对象，不支持多个附加入口对象。
- 不管理附加入口指向的后端 Service，这些 Service 由外部系统创建。
- 不支持为附加入口配置独立 host。
- 不为后端 Service 名称追加 `deployGroup.role`，也不按模板生成 Service 名称。

## API 形态

在 `api/v1alpha1/gameservice_types.go` 中新增类型：

```go
type ExtraIngressConfig struct {
    // +kubebuilder:validation:Required
    // +kubebuilder:validation:MinLength=1
    Name string `json:"name"`

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
    ServiceName string `json:"serviceName"`

    // +kubebuilder:validation:Required
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:validation:Maximum=65535
    Port int32 `json:"port"`
}
```

在 `GameServiceSpec` 中新增可选字段：

```go
ExtraIngress *ExtraIngressConfig `json:"extraIngress,omitempty"`
```

`ExtraIngressConfig` 不包含 `IngressClassName` 和 `TLS` 字段。原因是这两个配置只对
Ingress 有意义，并且需要与主 Ingress 保持一致，所以统一复用 `spec.ingress`。
`annotations` 可以单独配置，用于满足附加入口与主入口不同的注解需求。

`name` 是附加入口资源名的基础名，不是最终 Kubernetes 对象名称。最终对象名称格式为：

```text
{spec.extraIngress.name}-{strings.ToLower(effectiveTrafficMode(gs))}-{spec.deployGroup.role}
```

示例：

- `name: gamelogin`、`trafficMode: Ingress`、`role: blue` 生成 `gamelogin-ingress-blue`
- `name: gamelogin`、`trafficMode: Gateway`、`role: green` 生成 `gamelogin-gateway-green`

示例配置：

```yaml
spec:
  trafficMode: Ingress
  route:
    host: adventure.zzrr.io
    pathType: Prefix
    pathPrefix: /connector
    port: 3010
  ingress:
    ingressClassName: higress
    annotations:
      higress.ingress.kubernetes.io/proxy-read-timeout: "300s"
      higress.ingress.kubernetes.io/proxy-send-timeout: "300s"
  extraIngress:
    name: gamelogin
    annotations:
      higress.ingress.kubernetes.io/proxy-read-timeout: "300s"
    paths:
      - pathType: Prefix
        path: /serverlogin
        serviceName: logingame-blue
        port: 9020
      - pathType: Prefix
        path: /servergovern
        serviceName: servergovern-blue
        port: 9015
      - pathType: Prefix
        path: /backend
        serviceName: backend-blue
        port: 9010
      - pathType: Prefix
        path: /statsgm
        serviceName: statsgm-blue
        port: 9012
```

当切换到 green 版本时，green 的 `GameService` 可以直接配置可调用的 green Service：

```yaml
extraIngress:
  name: gamelogin
  paths:
    - pathType: Prefix
      path: /serverlogin
      serviceName: logingame-green
      port: 9020
```

如果后端服务本身使用稳定 Service 名称，也可以直接配置稳定名称。operator 不关心
Service 名称是否包含 blue 或 green，只负责引用当前 active `GameService` 中配置的
`serviceName`。

## 控制器行为

新增一组附加入口管理方法。可以放在现有 `IngressManager` 和 `HTTPRouteManager`
中，也可以提取成小的 helper，但实现范围应保持最小。

- `ReconcileExtraIngress(ctx, gs)`
- `DeleteExtraIngress(ctx, gs)`
- `ReconcileExtraHTTPRoute(ctx, gs)`
- `DeleteExtraHTTPRoute(ctx, gs)`

当 `spec.extraIngress` 为空时，上述方法都直接返回成功，不做任何操作。

当 `spec.deployGroup.active` 为 `true` 时，先按现有逻辑 reconcile 主流量入口，然后按
`effectiveTrafficMode(gs)` 处理附加入口。

附加入口资源名通过 helper 统一生成：

```go
func extraTrafficName(gs *zzrrv1alpha1.GameService, mode zzrrv1alpha1.TrafficMode) string {
    return fmt.Sprintf("%s-%s-%s", gs.Spec.ExtraIngress.Name, strings.ToLower(string(mode)), gs.Spec.DeployGroup.Role)
}
```

### Ingress 模式

当流量模式为 `Ingress` 时：

1. 删除 `HTTPRoute/<spec.connectorNamespace>/<name>-gateway-<role>`，作为从 Gateway
   模式切回 Ingress 模式时的陈旧资源清理。
2. 创建或更新 `Ingress/<spec.connectorNamespace>/<name>-ingress-<role>`。
3. 将 Ingress 的 `spec.ingressClassName` 设为 `spec.ingress.ingressClassName`。
4. 将唯一规则的 host 设为 `spec.route.host`。
5. 根据 `spec.extraIngress.paths` 构造 HTTP paths。
6. 将每个 path 的后端 Service 名设为 `path.serviceName`，端口设为 `path.port`。
7. 写入固定 labels，并复制 `spec.extraIngress.annotations`。
8. 如果 `spec.ingress.tls` 已配置，TLS hosts 使用 `spec.route.host`，secretName 使用
   `spec.ingress.tls.secretName`。

如果 `trafficMode` 为 `Ingress`，现有 CRD 校验已经要求 `spec.ingress` 存在，因此附加
Ingress 可以直接复用 `spec.ingress` 的 class 和 TLS 配置。

### Gateway 模式

当流量模式为 `Gateway` 时：

1. 删除 `Ingress/<spec.connectorNamespace>/<name>-ingress-<role>`，作为从 Ingress
   模式切到 Gateway 模式时的陈旧资源清理。
2. 创建或更新 `HTTPRoute/<spec.connectorNamespace>/<name>-gateway-<role>`。
3. ParentRef 复用 `spec.gateway.parentRef`。
4. Hostnames 使用 `spec.route.host`。
5. 根据 `spec.extraIngress.paths` 构造 HTTPRoute rules。
6. 将每条 rule 的 BackendRef name 设为 `path.serviceName`，port 设为 `path.port`。
7. 写入固定 labels，并复制 `spec.extraIngress.annotations`。

如果 `trafficMode` 为 `Gateway`，现有 CRD 校验已经要求 `spec.gateway` 存在，因此附加
HTTPRoute 可以直接复用 `spec.gateway.parentRef`。

## standby 和 finalizer 行为

当 `spec.deployGroup.active` 为 `false` 时：

1. 按现有逻辑删除主 Ingress 或 HTTPRoute。
2. 删除当前 role 对应的附加 Ingress 和附加 HTTPRoute：
   - `Ingress/<spec.connectorNamespace>/<name>-ingress-<role>`
   - `HTTPRoute/<spec.connectorNamespace>/<name>-gateway-<role>`

删除 `GameService` 进入 finalizer 时：

1. 按现有逻辑删除主 Ingress 和 HTTPRoute。
2. 删除当前 role 对应的附加 Ingress 和附加 HTTPRoute。
3. 继续执行现有 Service 清理和 finalizer 移除。

该行为遵循现有蓝绿删除语义。如果旧 active 的 `GameService` 先以 standby 状态
reconcile，而新 active 的 `GameService` 还未 reconcile，附加入口可能会短暂被删除。
这一点与当前主流量入口行为一致，是本次设计接受的行为。

因为附加入口资源名包含 trafficMode 和 role，所以蓝、绿资源天然分离。standby 和
finalizer 删除自身 role 的附加入口，不会删除另一个 role 的 active 资源。

## Labels 和 OwnerReference

附加 Ingress 和附加 HTTPRoute 使用以下 labels，与现有主入口区分：

```yaml
app.kubernetes.io/managed-by: gs-operator
gs-role: <deployGroup.role>
gs-extra-traffic: "true"
```

仅当 `GameService` 与 `spec.connectorNamespace` 位于同一 namespace 时设置
OwnerReference，保持现有主入口行为一致。跨 namespace 管理时依赖 labels，因为
Kubernetes 不允许跨 namespace OwnerReference。

## 错误处理和状态

active 组 reconcile 附加 Ingress 失败时：

- 记录 warning event，reason 为 `ExtraIngressReconcileFailed`
- 设置 `Available=False`，reason 为 `ExtraIngressReconcileFailed`
- 返回错误，让 reconcile 自动重试

active 组 reconcile 附加 HTTPRoute 失败时：

- 记录 warning event，reason 为 `ExtraHTTPRouteReconcileFailed`
- 设置 `Available=False`，reason 为 `ExtraHTTPRouteReconcileFailed`
- 返回错误，让 reconcile 自动重试

standby 组删除附加入口失败时：

- 记录 warning event，reason 为 `ExtraTrafficDeleteFailed`
- 设置 `Available=False`，reason 为 `ExtraTrafficDeleteFailed`
- 尽量保持当前 standby 清理逻辑的行为风格

附加入口成功时，现有主流量模式的 `Available=True` reason 可以保持不变。只有在不引入
大范围状态字段变动的前提下，才在 message 中补充附加入口信息。

## 测试

新增聚焦的 controller 测试：

- `trafficMode: Ingress` 且 active 时创建附加 Ingress。
- 附加 Ingress 使用 `spec.route.host` 作为 host。
- 附加 Ingress 复用 `spec.ingress.ingressClassName`。
- 附加 Ingress 复用 `spec.ingress.tls`。
- 附加 Ingress 使用 `spec.extraIngress.annotations`，不复用主 Ingress annotations。
- 附加 Ingress 后端 Service 名直接等于配置的 `serviceName`。
- `trafficMode: Gateway` 且 active 时创建附加 HTTPRoute。
- 附加 HTTPRoute 复用 `spec.gateway.parentRef`。
- 附加 HTTPRoute Hostnames 使用 `spec.route.host`。
- 附加 HTTPRoute BackendRef name 直接等于配置的 `serviceName`。
- active 模式切换时会清理当前 role 下另一个 trafficMode 的陈旧附加入口。
- standby `GameService` 会删除自身 role 对应的附加 Ingress 和附加 HTTPRoute。
- standby `GameService` 不会删除其他 role 的附加入口。
- 未配置 `spec.extraIngress` 时保持现有行为不变。

验证命令：

```bash
make manifests
make generate
make test
```

## 已确认决策

- 每个 `GameService` 最多配置一个附加入口。
- `spec.extraIngress.name` 是资源名基础名，不是最终对象名。
- 附加入口最终资源名格式为 `{name}-{strings.ToLower(trafficMode)}-{role}`。
- 附加入口 namespace 始终是 `spec.connectorNamespace`。
- 附加入口 host 始终是 `spec.route.host`。
- `trafficMode: Ingress` 时管理 Ingress。
- `trafficMode: Gateway` 时管理 HTTPRoute。
- 附加 Ingress 的 class 和 TLS 复用 `spec.ingress`。
- 附加入口 annotations 使用 `spec.extraIngress.annotations`，可以与主入口不同。
- 后端 Service 名直接来自 `spec.extraIngress.paths[].serviceName`。
- operator 不创建、不生成、不拼接附加入口后端 Service 名称。
- standby 和 finalizer 删除当前 role 对应的附加 Ingress 和附加 HTTPRoute。
