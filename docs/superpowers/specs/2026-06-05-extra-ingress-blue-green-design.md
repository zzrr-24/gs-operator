# 附加 Ingress 蓝绿切换设计

## 背景

`GameService` 目前会根据部署组的 active 状态，为主游戏服务创建或删除受管的
Ingress 或 HTTPRoute，从而完成蓝绿流量切换。

现在需要新增一个 `gamelogin` 入口。它属于另一套后端服务，但必须跟随同一个
`GameService` 蓝绿切换一起切换。该后端也有蓝、绿两套代码版本，因此 Ingress
后端 Service 名称需要根据 `spec.deployGroup.role` 自动生成。

## 目标

- 在 `GameService` 中新增可选字段 `spec.extraIngress`。
- 附加 Ingress 的 host 固定复用 `spec.route.host`。
- 附加 Ingress 的 namespace 固定复用 `spec.connectorNamespace`。
- 每个后端 Service 名称按 `{serviceBaseName}-{spec.deployGroup.role}` 生成。
- 无论 `trafficMode` 是 `Ingress` 还是 `Gateway`，都 reconcile 附加 Ingress。
- 未配置 `spec.extraIngress` 时保持现有行为不变。

## 非目标

- 不新增 CRD。
- 本次只支持一个附加 Ingress，不支持多个附加 Ingress 对象。
- 不管理附加 Ingress 指向的后端 Service，这些 Service 由外部系统创建。
- 不支持为附加 Ingress 配置独立 host。

## API 形态

在 `api/v1alpha1/gameservice_types.go` 中新增类型：

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

在 `GameServiceSpec` 中新增可选字段：

```go
ExtraIngress *ExtraIngressConfig `json:"extraIngress,omitempty"`
```

示例配置：

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

当 `deployGroup.role: blue` 时，生成的后端为：

- `/serverlogin` -> `logingame-blue:9020`
- `/servergovern` -> `servergovern-blue:9015`
- `/backend` -> `backend-blue:9010`
- `/statsgm` -> `statsgm-blue:9012`

## 控制器行为

在现有 `IngressManager` 中新增两个方法：

- `ReconcileExtraIngress(ctx, gs)`
- `DeleteExtraIngress(ctx, gs)`

当 `spec.extraIngress` 为空时，两个方法都直接返回成功，不做任何操作。

当 `spec.deployGroup.active` 为 `true` 时：

1. 先按现有逻辑 reconcile 主流量入口。
2. 创建或更新 `Ingress/<spec.connectorNamespace>/<spec.extraIngress.name>`。
3. 将 Ingress 的 `spec.ingressClassName` 设为
   `spec.extraIngress.ingressClassName`。
4. 将唯一规则的 host 设为 `spec.route.host`。
5. 根据 `spec.extraIngress.paths` 构造 HTTP paths。
6. 将每个 path 的后端 Service 名设为
   `{path.serviceBaseName}-{spec.deployGroup.role}`。
7. 写入固定 labels，并复制用户配置的 annotations。
8. 如果配置了 `spec.extraIngress.tls`，TLS hosts 使用 `spec.route.host`。

当 `spec.deployGroup.active` 为 `false` 时：

1. 按现有逻辑删除主 Ingress 或 HTTPRoute。
2. 仅当现有附加 Ingress 的 `gs-role` label 仍然等于当前 `GameService` 的
   role 时，才删除该附加 Ingress。

删除 `GameService` 进入 finalizer 时：

1. 按现有逻辑删除主 Ingress 和 HTTPRoute。
2. 删除配置的附加 Ingress。
3. 继续执行现有 Service 清理和 finalizer 移除。

该行为遵循现有蓝绿删除语义。如果旧 active 的 `GameService` 先以 standby 状态
reconcile，而新 active 的 `GameService` 还未 reconcile，附加 Ingress 可能会短暂
被删除。这一点与当前主流量入口行为一致，是本次设计接受的行为。

因为附加 Ingress 使用配置的固定名称，而不是现有 `game-ingress-blue` 和
`game-ingress-green` 这种按角色区分的名称，所以 standby 删除时必须检查
`gs-role` label。这样可以避免 standby 的 `GameService` 删除已经被当前 active
`GameService` 创建或更新过的附加 Ingress。

## Labels 和 OwnerReference

附加 Ingress 使用以下 labels，与现有主 Ingress 区分：

```yaml
app.kubernetes.io/managed-by: gs-operator
gs-role: <deployGroup.role>
gs-extra-ingress: "true"
```

仅当 `GameService` 与 `spec.connectorNamespace` 位于同一 namespace 时设置
OwnerReference，保持现有 Ingress 行为一致。跨 namespace 管理时依赖 labels，
因为 Kubernetes 不允许跨 namespace OwnerReference。

## 错误处理和状态

active 组 reconcile 附加 Ingress 失败时：

- 记录 warning event，reason 为 `ExtraIngressReconcileFailed`
- 设置 `Available=False`，reason 为 `ExtraIngressReconcileFailed`
- 返回错误，让 reconcile 自动重试

standby 组删除附加 Ingress 失败时：

- 记录 warning event，reason 为 `ExtraIngressDeleteFailed`
- 设置 `Available=False`，reason 为 `ExtraIngressDeleteFailed`
- 尽量保持当前 standby 清理逻辑的行为风格

附加 Ingress 成功时，现有主流量模式的 `Available=True` reason 可以保持不变。
只有在不引入大范围状态字段变动的前提下，才在 message 中补充附加 Ingress 信息。

## 测试

新增聚焦的 controller 测试：

- active `GameService` 会创建附加 Ingress，并使用 `spec.route.host` 作为 host
- active `GameService` 会按 `{serviceBaseName}-{deployGroup.role}` 生成后端 Service 名
- `trafficMode: Gateway` 时仍会 reconcile 附加 Ingress
- standby `GameService` 会删除 role label 匹配的附加 Ingress
- standby `GameService` 不会删除已经由其他 active role 接管的附加 Ingress
- 未配置 `spec.extraIngress` 时保持现有行为不变

验证命令：

```bash
make manifests
make generate
make test
```

## 已确认决策

- 每个 `GameService` 最多配置一个附加 Ingress。
- Ingress 名称来自 `spec.extraIngress.name`。
- Ingress namespace 始终是 `spec.connectorNamespace`。
- Ingress host 始终是 `spec.route.host`。
- Service 名格式为 `{serviceBaseName}-{deployGroup.role}`。
- standby 仅在现有对象的 `gs-role` label 与自身 role 匹配时删除附加 Ingress。
