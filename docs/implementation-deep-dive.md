# gs-operator 实现详解

本文档详细介绍 gs-operator 的实现思路、代码架构和关键技术决策。

---

## 目录

1. [核心架构](#1-核心架构)
2. [CRD 设计详解](#2-crd-设计详解)
3. [Reconcile 流程](#3-reconcile-流程)
4. [Connector Service 管理](#4-connector-service-管理)
5. [Ingress 动态管理](#5-ingress-动态管理)
6. [蓝绿发布实现](#6-蓝绿发布实现)
7. [Pod Watch 机制](#7-pod-watch-机制)
8. [保留期自动清理](#8-保留期自动清理)
9. [RBAC 权限设计](#9-rbac-权限设计)
10. [关键技术决策](#10-关键技术决策)

---

## 1. 核心架构

### 整体设计

```
┌─────────────────────────────────────────────────────────────────┐
│                      Kubernetes Cluster                        │
│                                                                │
│  ┌──────────────────────┐   ┌──────────────────────────────┐   │
│  │     GameService CR    │   │    GameService CR           │   │
│  │  name: blue           │   │  name: green                │   │
│  │  active: true         │   │  active: false              │   │
│  │  connectorNs: blue    │   │  connectorNs: green         │   │
│  └──────────┬───────────┘   └──────────┬───────────────────┘   │
│             │                          │                        │
│             └──────────┬───────────────┘                        │
│                        ▼                                        │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │              gs-operator (Controller)                    │   │
│  │                                                         │   │
│  │  Reconcile: ① 获取 Pod → ② 创建 Service → ③ 管理 Ingress │   │
│  └─────────────────────────────────────────────────────────┘   │
│                        │                                        │
│         ┌──────────────┼──────────────┐                        │
│         ▼              ▼              ▼                        │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐                  │
│  │ Pod       │  │ Service   │  │ Ingress   │                  │
│  │ connector │  │ per-pod   │  │ dynamic   │                  │
│  │ watch     │  │ manage    │  │ path list │                  │
│  └───────────┘  └───────────┘  └───────────┘                  │
└─────────────────────────────────────────────────────────────────┘
```

### 职责分离

Operator 的三个核心职责被拆分为三个独立的文件，每个文件一个明确的职责：

| 文件 | 职责 | 核心操作 |
|------|------|---------|
| `gameservice_controller.go` | 总控调度 | Reconcile 入口、条件更新、保留期管理 |
| `connector_service.go` | Pod 与 Service | 列举 Pod、创建/删除 Service |
| `ingress_manager.go` | Ingress 管理 | 创建/更新/删除 Ingress、Path 列表同步 |

### 为什么不把这些逻辑合并？

三个文件各自聚焦一个领域：
- `connector_service.go` 只关心 Pod 与 Service 的关系
- `ingress_manager.go` 只关心 Ingress 资源的构建
- `gameservice_controller.go` 只关心业务流程编排

这样每个文件可以独立理解和测试，修改一个不影响另一个。

---

## 2. CRD 设计详解

### GameServiceSpec

```go
type GameServiceSpec struct {
    Ingress            IngressConfig      `json:"ingress"`            // Ingress 配置
    ConnectorNamespace string             `json:"connectorNamespace"` // 目标命名空间
    DeployGroup        DeployGroupConfig  `json:"deployGroup"`        // 蓝绿发布配置
    Retention          *RetentionConfig   `json:"retention,omitempty"`// 保留策略
}
```

#### IngressConfig

```go
type IngressConfig struct {
    Host             string            `json:"host"`
    IngressClassName string            `json:"ingressClassName"`
    PathType         string            `json:"pathType"`
    PathPrefix       string            `json:"pathPrefix"`
    Port             int32             `json:"port"`
    TLS              *TLSConfig        `json:"tls,omitempty"`
    Annotations      map[string]string `json:"annotations,omitempty"`
}
```

**设计思路：** IngressConfig 的字段与 Kubernetes Ingress Spec 的对应关系：

```
CR 字段                    → Ingress 字段
ingress.host              → spec.rules[0].host
ingress.ingressClassName  → spec.ingressClassName
ingress.pathType          → spec.rules[0].http.paths[].pathType
ingress.pathPrefix + pod ordinal
                           → spec.rules[0].http.paths[].path
ingress.port              → spec.rules[0].http.paths[].backend.service.port.number
ingress.tls.secretName    → spec.tls[].secretName
ingress.annotations       → metadata.annotations
```

Annotations 做成 `map[string]string` 透传而不是固化为具体字段，是为了兼容不同的 Ingress Controller（Higress、nginx-ingress 等各有不同的注解）。

#### DeployGroupConfig

```go
type DeployGroupConfig struct {
    Role   string `json:"role"`   // "blue" | "green"
    Active bool   `json:"active"` // true=接收流量
}
```

`active` 是整个蓝绿切换的触发开关。Operator 不关心版本号，只通过 active 判断当前应该由哪个 Ingress 接收流量。

#### RetentionConfig

```go
type RetentionConfig struct {
    Enabled         bool   `json:"enabled"`
    DefaultDuration string `json:"defaultDuration"` // 如 "24h"
}
```

保留期是蓝绿发布的安全网。非活跃环境保留一段时间供回滚，到期后 Operator 自动清理。

---

## 3. Reconcile 流程

### 完整流程

```
Reconcile(Request)
    │
    ├─ 1. 获取 GameService 实例
    │     └─ 不存在 → 返回（资源已删除）
    │
    ├─ 2. 列举 ConnectorNamespace 中所有 Connector Pod
    │     └─ 按标签 adventure=connector 筛选
    │
    ├─ 3. 过滤running/pending状态的 Pod，提取序号
    │
    ├─ 4. 对每个 Pod 确保 Serivice 存在
    │     └─ ServiceName: connector-{ordinal}-svc
    │
    ├─ 5. 删除孤儿 Service（对应 Pod 已经不存在的）
    │
    ├─ 6. 判断 deployGroup.active:
    │     ├─ true  → 创建/更新 Ingress
    │     └─ false → 删除 Ingress
    │
    ├─ 7. 更新 Status Conditions
    │
    ├─ 8. 处理保留期逻辑
    │     └─ active=false + retention.enabled:
    │         ├─ 未到期 → RequeueAfter（定时检查）
    │         └─ 已到期 → 删除 CR 和 Ingress
    │
    └─ 9. 返回
```

### 触发条件

```go
// 通过 SetupWithManager 注册
func (r *GameServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&zzrrv1alpha1.GameService{}).              // GameService CR 变更
        Watches(
            &corev1.Pod{},
            handler.EnqueueRequestsFromMapFunc(r.mapConnectorPodToGameService),  // Pod 变更
        ).
        Named("gameservice").
        Complete(r)
}
```

为什么 Watch Pod？因为 Connector Pod 是由外部（ArgoCD/用户）管理的 StatefulSet，Operator 需要感知 Pod 创建/删除来调整 Service 和 Ingress。

### mapConnectorPodToGameService

```go
func (r *GameServiceReconciler) mapConnectorPodToGameService(ctx, obj) []reconcile.Request {
    // 只处理 adventure=connector 标签的 Pod
    // 查找 connectorNamespace 匹配的 GameService CR
    // 返回这些 CR 的 Request
}
```

这实现了 **Pod → CR 的关联**：当 connector 命名空间中 Pod 变化时，自动触发相关 CR 的 Reconcile。

---

## 4. Connector Service 管理

### 设计思路

StatefulSet 的 Pod 有稳定的网络标识 `{statefulset}-{ordinal}`，但默认只有 headless Service 可以访问到单个 Pod。为了让 Ingress 可以路由到特定 Pod，需要为每个 Pod 创建一个独立的 ClusterIP Service。

### service 创建逻辑

```go
func (m *ConnectorServiceManager) EnsureService(ctx, pod, port) (*Service, error) {
    ordinal := GetPodOrdinal(pod.Name)
    svcName := fmt.Sprintf("connector-%s-svc", ordinal)

    // 先检查是否已存在
    var existingSvc Service
    if err := m.Get(ctx, {svcName, pod.Namespace}, &existingSvc); err == nil {
        return &existingSvc, nil  // 已存在，直接返回
    }

    // 不存在则创建
    svc := &Service{
        Selector: {"statefulset.kubernetes.io/pod-name": pod.Name},  // 精确匹配单个 Pod
        Ports: [{Port: port, TargetPort: port}],
    }
    return m.Create(ctx, svc)
}
```

关键点：**`statefulset.kubernetes.io/pod-name` 是 K8s StatefulSet Controller 自动注入的标准标签**，精确匹配到单个 Pod，不需要自定义标签。

### 孤儿 Service 清理

```go
func (m *ConnectorServiceManager) DeleteOrphanServices(ctx, namespace, activeOrdinals) {
    // 列举所有 gs-operator 管理的 Service
    // 对比 activeOrdinals 列表
    // 删除不在列表中的 Service
}
```

通过标签 `app.kubernetes.io/managed-by=gs-operator` 识别哪些 Service 是由 Operator 创建的，避免误删用户手动创建的 Service。

### Pod 序号提取

```go
func GetPodOrdinal(podName string) string {
    // 从 pod 名称 "connector-5" 提取 "5"
    // 从最后一个 "-" 之后取子串
    for i := len(podName) - 1; i >= 0; i-- {
        if podName[i] == '-' {
            return podName[i+1:]
        }
    }
    return ""
}
```

---

## 5. Ingress 动态管理

### Ingress 结构

Operator 为每个 GameService CR（每个角色）在 connector 所在命名空间中维护一个 Ingress。格式：

```
Ingress Name: game-ingress-{role}
Namespace: connectorNamespace
Path Pattern: /{pathPrefix}{ordinal}
```

### 构建逻辑

```go
func (m *IngressManager) ReconcileIngress(ctx, gs, ordinals) error {
    ingressName := fmt.Sprintf("game-ingress-%s", gs.Spec.DeployGroup.Role)

    paths := []HTTPIngressPath{}
    for _, ord := range ordinals {
        paths = append(paths, HTTPIngressPath{
            Path:     fmt.Sprintf("%s%s", gs.Spec.Ingress.PathPrefix, ord),
            PathType: pathType,
            Backend: IngressBackend{
                Service: &IngressServiceBackend{
                    Name: fmt.Sprintf("connector-%s-svc", ord),
                    Port: {Number: gs.Spec.Ingress.Port},
                },
            },
        })
    }

    // 构建 Ingress 对象
    desiredIngress := &Ingress{
        Annotations: gs.Spec.Ingress.Annotations,          // 注解透传
        IngressClassName: gs.Spec.Ingress.IngressClassName,
        Rules: [{Host: gs.Spec.Ingress.Host, HTTP: {Paths: paths}}],
        TLS: tlsConfig,
    }

    // 创建或更新
    if exists {
        existing.Spec = desiredIngress.Spec
        existing.Annotations = desiredIngress.Annotations
        existing.Labels = desiredIngress.Labels
        m.Update(ctx, &existing)
    } else {
        m.Create(ctx, desiredIngress)
    }
}
```

### 为什么 Ingress 创建在 ConnectorNamespace 中？

Kubernetes Ingress 的 `backend.service` 只能引用同命名空间的 Service。如果 Ingress 在 `default` 命名空间，`service: connector-0-svc` 只能解析到 `default` 命名空间的 Service。

因此 Ingress 必须和 Service 在同一个命名空间。Operator 使用 CR 的 `connectorNamespace` 字段作为 Ingress 的目标命名空间。

### 跨命名空间 OwnerReference 的处理

由于 Ingress 和 GameService CR 可能在不同命名空间，`controllerutil.SetControllerReference` 不允许跨命名空间设置 OwnerReference。处理方式：

```go
if gs.Namespace == gs.Spec.ConnectorNamespace {
    // 同命名空间时设置 OwnerReference，实现级联删除
    controllerutil.SetControllerReference(gs, desiredIngress, m.Scheme)
}
// 跨命名空间时通过 Labels 管理
```

---

## 6. 蓝绿发布实现

### 核心机制

蓝绿发布通过 `deployGroup.active` 字段控制：

```
Blue.active = true  → 创建 game-ingress-blue 到 adventure-blue
Green.active = false → 删除 game-ingress-green（如果存在）

                     ↓ 手动 Patch ↓

Blue.active = false → 删除 game-ingress-blue
Green.active = true  → 创建 game-ingress-green 到 adventure-green
```

### 切换的幂等性

```go
if gs.Spec.DeployGroup.Active {
    // 创建/更新 Ingress
    ingMgr.ReconcileIngress(ctx, &gs, ordinals)
} else {
    // 删除 Ingress（如果有）
    ingMgr.DeleteIngress(ctx, &gs)
}
```

这样设计的好处：
- 多次 Patch 同一状态是安全的（幂等）
- 不存在两个 Ingress 争抢相同 Host 的问题
- 切换时短暂的双 active 不会导致流量错误

### 两个 CR 都 active 的处理

切换操作是两个 Patch 命令：

```bash
kubectl patch gameservice green --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice blue --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
```

两个命令之间短暂存在双 active 的间隙。Operator 的行为：

```
t1: blue.active=true (Ingress exists)
    green.active=false (no Ingress)

t2: blue.active=true (Ingress exists)   ← 第一次 patch
    green.active=true (create Ingress)     green Ingress 创建
    → 此时两个 Ingress 都存在但指向不同命名空间

t3: blue.active=false (delete Ingress!) ← 第二次 patch
    green.active=true (Ingress exists)     blue Ingress 删除
    → 只有 green Ingress 存在
```

在 t2 时刻，Higress/Envoy 可能会在两个 Ingress 之间负载均衡。但在实际场景中，切换间隙极短（毫秒级），且新连接也只持续到 t3 前的极短时间。对于 WebSocket 场景，已有连接不受影响。

### 保留期倒计时

```go
if !deployGroup.Active && retention.Enabled {
    if creationTime + duration < now {
        // 到期：删除 Ingress + 删除 CR
        ingMgr.DeleteIngress(ctx, &gs)
        r.Delete(ctx, &gs)
        return
    }
    // 未到期：定时唤醒检查
    requeueAfter = creationTime + duration - now
    return ctrl.Result{RequeueAfter: requeueAfter}
}
```

`ctrl.Result{RequeueAfter: requeueAfter}` 告诉 controller-runtime 在指定时间后再次 Reconcile，实现定时唤醒检查。

---

## 7. Pod Watch 机制

### 为什么需要 Watch Pod？

Connector StatefulSet 由 ArgoCD 或用户管理，Operator 不直接控制 Pod 的生命周期。但 Operator 需要感知 Pod 变化来：

1. **Pod 创建** → 创建对应的 Service → 添加到 Ingress Path
2. **Pod 删除** → 删除对应的 Service → 从 Ingress Path 移除
3. **Pod 重建** → Service 自动更新 Endpoint（通过 Selector）

### 实现方式

```go
Watches(
    &corev1.Pod{},
    handler.EnqueueRequestsFromMapFunc(r.mapConnectorPodToGameService),
)
```

这种方式叫做 **Cross-Resource Watch**：监听 Pod 变化，映射到 GameService CR 的 Reconcile Request。

### 筛选逻辑

```go
func (r *GameServiceReconciler) mapConnectorPodToGameService(ctx, obj) []reconcile.Request {
    // 1. 只处理 adventure=connector 标签的 Pod
    if pod.Labels["adventure"] != "connector" {
        return nil
    }

    // 2. 查找所有 GameService CR
    var list zzrrv1alpha1.GameServiceList
    r.List(ctx, &list)

    // 3. 只触发 connectorNamespace 匹配的 CR
    for _, gs := range list.Items {
        if gs.Spec.ConnectorNamespace == pod.Namespace {
            requests = append(requests, Request{...})
        }
    }
    return requests
}
```

---

## 8. 保留期自动清理

### 触发时机

保留期清理在每次 Reconcile 的非活跃分支中执行：

```go
if !gs.Spec.DeployGroup.Active && gs.Spec.Retention != nil && gs.Spec.Retention.Enabled {
    // 检查是否到期
    duration, _ := time.ParseDuration(gs.Spec.Retention.DefaultDuration)
    if gs.CreationTimestamp.Add(duration).Before(time.Now()) {
        // 到期：先删 Ingress，再删 CR
        ingMgr.DeleteIngress(ctx, &gs)
        r.Delete(ctx, &gs)
        return ctrl.Result{}, nil
    }
    // 未到期：设置 RequeueAfter 定时检查
    requeueAfter := time.Until(gs.CreationTimestamp.Add(duration))
    return ctrl.Result{RequeueAfter: requeueAfter}, nil
}
```

### 为什么不使用 Finalizer？

Finalizer 适合资源删除前的清理动作。这里使用的是**定时到期自动删除**，用 `RequeueAfter` + 时间判断更直接。

### 用户提前删除

用户执行 `kubectl delete gameservice blue` 时，由于 Ingress 和 CR 可能在不同命名空间（跨 namespace 未设置 OwnerReference），Ingress 不会被级联删除。因此 Operator 需要在 Reconcile 中处理资源不存在的情况：

```go
if err := r.Get(ctx, req.NamespacedName, &gs); err != nil {
    if apierrors.IsNotFound(err) {
        return ctrl.Result{}, nil  // CR 已被删除
    }
    return ctrl.Result{}, err
}
```

---

## 9. RBAC 权限设计

### 所需权限

```go
// +kubebuilder:rbac:groups=zzrr.gs.zzrr.io,resources=gameservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=zzrr.gs.zzrr.io,resources=gameservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=zzrr.gs.zzrr.io,resources=gameservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
```

为什么要分这么细？

| 资源 | 操作 | 原因 |
|------|------|------|
| `gameservices` | CRUD | 管理 CR 生命周期 |
| `gameservices/status` | read+update | 更新 Status Conditions |
| `pods` | read only | 只列举和 Watch Pod，不创建 |
| `services` | CRUD | 创建/删除/更新 per-pod Service |
| `ingresses` | CRUD | 创建/删除/更新 Ingress |
| `events` | create+patch | 记录 Warning/Error 事件 |

**最小权限原则**：只申请需要的权限。比如 Pod 只有 `get;list;watch` 没有 `create;update;delete`，因为 Operator 不管理 Pod 本身。

---

## 10. 关键技术决策

### 为什么用 ClusterIP Service 而不是 Headless Service？

Headless Service（`clusterIP: None`）的 DNS 解析直接返回 Pod IP，适合 StatefulSet 内部通信。但 Ingress 需要 ClusterIP Service 作为稳定的入口。

每个 Connector Pod 一个 ClusterIP Service 的设计，让 Ingress 可以通过 Service 名称精确路由到指定 Pod。

### 为什么 Ingress 放在 ConnectorNamespace？

Kubernetes Ingress 规范要求 `backend.service.name` 指向同一命名空间的 Service。如果 Ingress 和 Service 在不同命名空间，需要 Ingress Controller 的扩展机制（如 Higress McpBridge）才能工作。为保持通用性，选择将 Ingress 放在 Connector 的命名空间。

### 为什么不用 Helm 管理两个 Namespace 的部署？

蓝绿发布中的两个命名空间包含完整的游戏服（master/insideapi/webapi/connector），这些由 ArgoCD 管理。Operator 只负责 Ingress 层。职责分离的好处是：

- Operator 不需要知道游戏服的具体配置
- 游戏服变更不需要修改 Operator
- 蓝绿切换不涉及 ArgoCD 应用的变化

### 为什么 Watch Pod 而不是 Service？

理论上 Watch Service 更直接（Service 变化时触发 Reconcile）。但 Pod 变化是更早的信号——Pod 创建后 Operator 可以立即创建 Service 和更新 Ingress。等 Service 变化再触发就慢了一步。

### Higress 跨 Namespace 路由说明

测试环境中 Higress 通过 NodePort 31546 暴露。实际生产环境中：

- Higress Gateway 通常有 LoadBalancer IP
- Ingress 的 Host 通过 DNS 解析到 Gateway IP
- Higress 根据 Ingress 规则路由到对应命名空间的 Service

### WebSocket 支持

Connector 通常使用 WebSocket 连接。Ingress 需要正确设置代理超时参数：

```yaml
annotations:
  higress.ingress.kubernetes.io/proxy-read-timeout: "300s"
  higress.ingress.kubernetes.io/proxy-send-timeout: "300s"
```

这些注解通过 CR 的 `ingress.annotations` 透传到 Ingress，Operator 不做特殊处理。

---

## 附录：文件清单

| 文件 | 行数 | 说明 |
|------|------|------|
| `api/v1alpha1/gameservice_types.go` | ~62 | CRD 类型定义 |
| `internal/controller/gameservice_controller.go` | ~200 | Reconcile 主逻辑 |
| `internal/controller/connector_service.go` | ~110 | Service 管理 |
| `internal/controller/ingress_manager.go` | ~140 | Ingress 管理 |
| `cmd/main.go` | ~204 | 入口、注册 Controller |
| `config/rbac/role.yaml` | ~71 | RBAC 权限（自动生成） |
| `config/crd/bases/zzrr.gs.zzrr.io_gameservices.yaml` | ~300+ | CRD 定义（自动生成） |
