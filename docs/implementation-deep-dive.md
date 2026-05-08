# gs-operator 实现深度解析

> 面向小白读者的完整项目解读，从零开始理解 Kubernetes Operator 如何工作。

---

## 目录

1. [这个 Operator 是干嘛的](#1-这个-operator-是干嘛的)
2. [核心概念：什么是蓝绿部署](#2-核心概念什么是蓝绿部署)
3. [Operator 整体架构](#3-operator-整体架构)
4. [自定义资源（CRD）：GameService](#4-自定义资源crdgameservice)
5. [控制器主循环：Reconcile](#5-控制器主循环reconcile)
6. [Service 管理](#6-service-管理)
7. [Ingress 管理](#7-ingress-管理)
8. [蓝绿切换与流量路由](#8-蓝绿切换与流量路由)
9. [保留策略（自动过期删除）](#9-保留策略自动过期删除)
10. [Finalizer 与优雅清理](#10-finalizer-与优雅清理)
11. [事件驱动机制](#11-事件驱动机制)
12. [构建与部署](#12-构建与部署)
13. [测试体系](#13-测试体系)
14. [踩坑记录与设计决策](#14-踩坑记录与设计决策)

---

## 1. 这个 Operator 是干嘛的

**一句话概括**：gs-operator 是一个 Kubernetes Operator，它为一组 StatefulSet 中的 Pod 自动创建 Headless Service，并把它们注册到 Ingress 上，支持**蓝绿部署**和**自动过期删除**。

**实际场景**：假设你有一组游戏服务器（connector pods），每个 Pod 都需要一个独立的网络入口。同时你希望：
- 有一个"蓝"环境和"绿"环境，可以无缝切换流量
- 不活跃的环境在 24 小时后自动清理

这个 Operator 就是干这个的。

### 它管理的资源

```
GameService CR (你写的 YAML)
    │
    ├── 自动创建/删除 Service（每个 connector Pod 一个）
    │     connector-0-svc → Pod connector-0
    │     connector-1-svc → Pod connector-1
    │     ...
    │
    └── 自动创建/更新 Ingress
         game-ingress-blue  → 包含所有 blue 环境 Pod 的路由路径
         game-ingress-green → 包含所有 green 环境 Pod 的路由路径
```

---

## 2. 核心概念：什么是蓝绿部署

蓝绿部署（Blue-Green Deployment）是一种零停机发布策略：

```
       用户请求
          │
    ┌─────▼─────┐
    │  Ingress   │  ← 入口网关，决定流量走向
    └─────┬─────┘
          │
    ┌─────▼─────┐
    │  active=   │  ← 当前活跃的部署组
    │  true      │
    └─────┬─────┘
          │
    ┌─────▼─────┐     ┌──────────┐
    │   Blue     │     │  Green   │  ← 备用部署组
    │  (活跃)    │     │ (待命中) │     active=false
    │  Pod 0-9   │     │ Pod 0-9  │
    └───────────┘     └──────────┘
```

- **活跃组**（active=true）：Ingress 指向这些 Pod，接收所有流量
- **备用组**（active=false）：Pod 运行但在 Ingress 上没有路由，等待切换
- **切换**：只需修改 CR 的 `spec.deployGroup.active` 字段，Operator 自动更新 Ingress

---

## 3. Operator 整体架构

```
┌─────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                    │
│                                                         │
│  ┌──────────────┐     ┌──────────────────┐              │
│  │ GameService  │────▶│  Controller      │              │
│  │    CR        │     │  (Reconciler)    │              │
│  └──────────────┘     └──────┬───────────┘              │
│                              │                           │
│              ┌───────────────┼───────────────┐          │
│              │               │               │          │
│         ┌────▼────┐   ┌─────▼──────┐  ┌─────▼──────┐  │
│         │ Service  │   │  Ingress   │  │    Pod     │  │
│         │ Manager  │   │  Manager   │  │  Watcher   │  │
│         └─────────┘   └────────────┘  └────────────┘  │
│                                                         │
│  ┌──────────────────────────────────────────────────┐  │
│  │  次级资源                                          │  │
│  │  ┌──────────┐  ┌──────────┐  ┌───────────────┐   │  │
│  │  │connector │  │connector │  │game-ingress-  │   │  │
│  │  │ -0-svc   │  │ -1-svc   │  │    blue       │   │  │
│  │  └──────────┘  └──────────┘  └───────────────┘   │  │
│  └──────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

### 项目文件结构

```
gs-operator/
├── api/v1alpha1/
│   └── gameservice_types.go      # CRD 结构定义
├── cmd/
│   └── main.go                    # 程序入口，启动 Manager
├── internal/controller/
│   ├── gameservice_controller.go  # 核心调和循环
│   ├── connector_service.go       # Service 创建/删除管理
│   └── ingress_manager.go         # Ingress 创建/更新/删除管理
├── config/
│   ├── crd/bases/                 # 自动生成的 CRD YAML
│   ├── rbac/                      # RBAC 权限配置
│   ├── manager/manager.yaml       # Deployment 定义
│   └── default/kustomization.yaml # Kustomize 聚合入口
├── Makefile
├── Dockerfile
└── PROJECT                        # Kubebuilder 元数据
```

---

## 4. 自定义资源（CRD）：GameService

### CRD 是什么

Kubernetes 原生资源（Pod、Service、Deployment）是写死的。如果你想要自己的资源类型，就得定义一个 **CRD（Custom Resource Definition）**——告诉 K8s "我要一种叫 GameService 的东西，它长这样"。

### GameService 的 Spec（期望状态）

用户创建一个 GameService YAML 来描述"我想要什么"：

```yaml
apiVersion: gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  name: blue
  namespace: gsb
spec:
  connectorNamespace: gsb           # connector Pod 所在的 namespace
  deployGroup:
    role: blue                      # 部署组名称（blue 或 green）
    active: true                    # 是否承载流量
  ingress:
    host: game.zzrr.io              # Ingress 域名
    ingressClassName: higress       # Ingress Controller 类型
    pathType: Prefix
    pathPrefix: /connector          # 路径前缀
    port: 80
  retention:                        # 保留策略（可选）
    enabled: true
    defaultDuration: 24h            # 不活跃后 24 小时自动删除
```

### Spec 字段详解

| 字段 | 类型 | 说明 |
|------|------|------|
| `connectorNamespace` | string | connector Pod 所在的 namespace |
| `deployGroup.role` | enum(blue/green) | 蓝绿部署组标识 |
| `deployGroup.active` | bool | true=承载流量，false=备用 |
| `ingress.host` | string | Ingress 的域名 |
| `ingress.ingressClassName` | string | 使用的 Ingress Controller（如 nginx、higress） |
| `ingress.pathType` | enum | 路径匹配类型（Prefix/Exact） |
| `ingress.pathPrefix` | string | 每个 connector 的 URL 前缀 |
| `ingress.port` | int32 | connector Pod 的服务端口 |
| `retention.enabled` | bool | 是否启用自动过期 |
| `retention.defaultDuration` | string | 过期时长，如 "24h" |

### Status（实际状态）

Operator 通过 Status 字段报告当前状况：

```yaml
status:
  conditions:
  - type: Available           # 资源是否可用
    status: "True"
    reason: AllIngressPathsReady
  - type: TrafficActive       # 是否正在承载流量
    status: "True"
    reason: Active
  connectorCount: 10          # 当前活跃的 connector 数量
  connectorImage: nginx:alpine # connector 容器镜像名
  observedGeneration: 5       # 已处理的 CR 版本号
```

### 代码中的类型定义

`api/v1alpha1/gameservice_types.go` 定义了 Go 结构体，通过 `+kubebuilder` 注释标记：

```go
// +kubebuilder:object:root=true           // ← 告诉 K8s: 这是一个顶层资源
// +kubebuilder:subresource:status         // ← 启用 /status 子资源（Status 独立更新）
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.deployGroup.role"
type GameService struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec              GameServiceSpec   `json:"spec"`
    Status            GameServiceStatus `json:"status,omitempty"`
}
```

这些注释在运行 `make manifests` 时被 `controller-gen` 读取，自动生成 CRD YAML。**不要手改生成的 YAML。**

---

## 5. 控制器主循环：Reconcile

### 什么是 Reconcile

Reconcile（调和）是 Operator 的核心——一种"看看现在是什么样子，改到期望的样子"的循环。

每当你创建、修改 GameService CR，或有 connector Pod 变化时，Kubernetes 会调用 `Reconcile` 方法。

### Reconcile 的完整流程

以下是 `gameservice_controller.go` 中 `Reconcile` 方法的执行步骤：

```
用户创建/修改 GameService CR
        │
        ▼
┌──────────────────────────────────┐
│ Step 1: Get GameService CR       │  从 API Server 获取最新状态
│                                 │
│ 如果 CR 已被删除？              │
│   └→ finalize() 清理所有资源    │
│                                 │
│ 如果没有 finalizer？            │
│   └→ 添加 finalizer，返回       │  finalizer 防止 CR 被直接删除
└────────┬─────────────────────────┘
         │
         ▼
┌──────────────────────────────────┐
│ Step 2: List Connector Pods      │  查找 connectorNamespace 下带
│                                 │  label "adventure=connector" 的 Pod
│  过滤条件:                       │
│  - Phase = Running 或 Pending    │
│  - 提取 ordinal（从 pod 名）     │  connector-0 → "0"
└────────┬─────────────────────────┘
         │
         ▼
┌──────────────────────────────────┐
│ Step 3: Ensure Services          │  并发 5 路，为每个 Pod 确保
│                                 │  对应的 Service 存在且正确
│  并发控制: semaphore(5)          │
│  错误处理: 单个失败不中断         │
└────────┬─────────────────────────┘
         │
         ▼
┌──────────────────────────────────┐
│ Step 4: Delete Orphan Services   │  删除没有对应 Pod 的 Service
│                                 │  （通过 ordinal 匹配）
└────────┬─────────────────────────┘
         │
         ▼
┌──────────────────────────────────┐
│ Step 5: Reconcile Ingress        │
│                                 │
│  如果 active=true:               │
│    → 创建/更新 Ingress           │  路径指向每个 connector-svc
│    → setCondition Available=True │
│    → setCondition TrafficActive  │
│        =True                     │
│                                 │
│  如果 active=false:              │
│    → 删除 Ingress               │
│    → setCondition TrafficActive  │
│        =False                    │
└────────┬─────────────────────────┘
         │
         ▼
┌──────────────────────────────────┐
│ Step 6: Update Status            │  更新 connectorCount、
│                                 │  connectorImage、observedGeneration
└────────┬─────────────────────────┘
         │
         ▼
┌──────────────────────────────────┐
│ Step 7: Retention Check          │
│                                 │
│  如果 active=false 且 retention   │
│  已启用:                         │
│    → 计算过期时间                │
│    → 如果已过期 → 删除 CR       │
│    → 如果未过期 → requeueAfter   │
└────────┬─────────────────────────┘
         │
         ▼
      返回 ctrl.Result{}
```

### 关键代码解读

**并发控制**：使用 `golang.org/x/sync/errgroup` + `semaphore` 实现最多 5 路并发的 Service 创建：

```go
const maxConcurrency = 5
sem := semaphore.NewWeighted(maxConcurrency)
eg, egCtx := errgroup.WithContext(ctx)

for i := range pods {
    pod := pods[i]
    eg.Go(func() error {
        sem.Acquire(egCtx, 1)     // 获取许可证，最多 5 个 goroutine
        defer sem.Release(1)
        // ... EnsureService ...
    })
}
eg.Wait()  // 等待所有 goroutine 完成
```

**为什么需要并发**：当 Pod 数量很大时（如 50 个），串行创建 Service 会非常慢。并发 5 路可以显著提速。

---

## 6. Service 管理

`connector_service.go` 负责管理每个 connector Pod 的专属 Service。

### Service 命名规则

```
Pod 名称: connector-0
  ↓
提取 ordinal: "0"（从最后一个 "-" 后面截取）
  ↓
Service 名称: connector-0-svc
```

### EnsureService 流程

```
EnsureService(ctx, pod, port)
        │
        ▼
  ┌────────────────┐
  │ 构建 desiredSvc │  ← 包含 Labels、OwnerReference、Selector、Ports
  └───────┬────────┘
          │
          ▼
  ┌──────────────────┐
  │ Get 现有 Service  │
  └───────┬──────────┘
          │
    存在？ │  不存在？
    ┌─────┴─────┐
    │           │
    ▼           ▼
  比较：      Create(desiredSvc)
  - Ports      → 日志："Created Service"
  - Labels
  - OwnerRef
    │
 需要更新？→ Update → 日志："Updated Service"
 不需要？  → 跳过
```

### OwnerReference 机制

**这是本项目的关键设计之一。** 每个 Service 都持有对应 Pod 的 OwnerReference：

```go
desiredSvc := &corev1.Service{
    ObjectMeta: metav1.ObjectMeta{
        OwnerReferences: []metav1.OwnerReference{
            {
                APIVersion:         "v1",
                Kind:               "Pod",
                Name:               pod.Name,   // connector-0
                UID:                pod.UID,    // Pod 的唯一 ID
                BlockOwnerDeletion: ptr.To(false),  // 不阻止 Pod 被删除
            },
        },
    },
}
```

**OwnerReference 的作用**：
- 当 StatefulSet 缩容删除 Pod 时，Kubernetes 垃圾回收器（GC）会自动级联删除对应的 Service
- `BlockOwnerDeletion: false` 确保 Pod 可以立即被删除，不必等待 Service 先删完
- 如果 Pod 被重新创建（新 UID），`ownerRefContains` 检查会发现 OwnerRef 不匹配，触发 Update

**补充**：Service 还通过 label selector `statefulset.kubernetes.io/pod-name` 精准指向目标 Pod，实现一对一绑定。

### 辅助函数

- `ownerRefContains(refs, uid)` — 检查 Service 是否已持有指定 Pod 的 OwnerReference
- `mergeOwnerRefs(existing, incoming, podUID)` — 合并新旧 OwnerReference，保留其他可能的 owner

### DeleteOrphanServices

除了 GC 自动清理外，Operator 也会在每次 Reconcile 时主动检查并删除孤儿 Service：

```go
func DeleteOrphanServices(ctx, namespace, activeOrdinals) {
    // 1. List 所有的 managed-by=gs-operator 标签的 Service
    // 2. 对每个 Service，提取其 gs-connector-ordinal label
    // 3. 如果该 ordinal 不在 activeOrdinals 中 → 删除
    // 4. 如果 Delete 返回 NotFound → 忽略（GC 已删除了）
}
```

**两层保障**：OwnerReference（GC 自动清理） + DeleteOrphanServices（Operator 主动清理），确保 Service 能被及时清除。

---

## 7. Ingress 管理

`ingress_manager.go` 负责管理 Ingress 资源。

### Ingress 命名规则

```
DeployGroup.Role = "blue" → Ingress 名称: game-ingress-blue
DeployGroup.Role = "green" → Ingress 名称: game-ingress-green
```

### 路径构建

每个 connector Pod 对应一条 Ingress 路径：

```
Pod ordinal "0" + PathPrefix "/connector" → 路径: /connector0
Pod ordinal "1" + PathPrefix "/connector" → 路径: /connector1
...
```

生成的 Ingress 路由规则类似：

```yaml
spec:
  rules:
  - host: game.zzrr.io
    http:
      paths:
      - path: /connector0
        backend:
          service:
            name: connector-0-svc
            port:
              number: 80
      - path: /connector1
        backend:
          service:
            name: connector-1-svc
            port:
              number: 80
      ...
```

### ReconcileIngress 流程

```
ReconcileIngress(ctx, gs, ordinals)
        │
        ▼
  ┌──────────────────┐
  │ 构建 desiredIngress│  ← 完整 paths、TLS、Labels、Annotations
  └───────┬──────────┘
          │
          ▼
  ┌──────────────────────┐
  │ 设置 OwnerReference  │  ← 仅当 gs.Namespace == connectorNamespace
  └───────┬──────────────┘
          │
          ▼
  ┌──────────────────┐
  │ Get 现有 Ingress  │
  └───────┬──────────┘
          │
    存在？ │  不存在？
    ┌─────┴─────┐
    │           │
    ▼           ▼
  覆盖：      Create → "Created Ingress"
  - Spec       │
  - Annotations│
  - Labels     │
    │           │
    ▼           ▼
  Update → "Updated Ingress, paths: N"
```

### OwnerReference 跨 Namespace 注意

OwnerReference 只能在同一 namespace 中生效。如果 `GameService` CR 的 namespace 和 `connectorNamespace` 不同，则**不设置** OwnerReference，仅依靠 Labels 管理跨 namespace 的 Ingress。

---

## 8. 蓝绿切换与流量路由

### 切换原理

蓝绿部署的核心是 **Ingress**。Operator 通过 Ingress 控制哪个部署组接收流量：

```
       用户请求 game.zzrr.io/connector0
                    │
              ┌─────▼─────┐
              │  Ingress   │
              └─────┬─────┘
                    │
         根据 game-ingress-{role} 决定路由
                    │
         ┌──────────┴──────────┐
         │                     │
    Blue 活跃时             Green 活跃时
    game-ingress-blue      game-ingress-green
    路由到 connector-*-svc   路由到 connector-*-svc
   (gsb namespace)        (gsg namespace)
```

### 切换时的 Reconcile 流程

```
kubectl patch gameservice blue -n gsb --type merge \
  -p '{"spec":{"deployGroup":{"active":false}}}'
  
kubectl patch gameservice green -n gsg --type merge \
  -p '{"spec":{"deployGroup":{"active":true}}}'
```

**Blue 的 Reconcile:**
1. `DeployGroup.Active = false`
2. → DeleteIngress（删除 game-ingress-blue）
3. → setCondition TrafficActive=False

**Green 的 Reconcile:**
1. `DeployGroup.Active = true`
2. → 列出 green namespace 下的 connector Pods
3. → 为每个 Pod 确保 Service
4. → ReconcileIngress（创建 game-ingress-green，包含所有路径）
5. → setCondition TrafficActive=True

**结果**：流量从 blue 无缝切换到 green。

### Status Condition 详解

| Condition 类型 | 什么时候 True | 什么时候 False |
|---------------|-------------|---------------|
| `Available` | Ingress 路径已同步 | Ingress 操作失败 |
| `TrafficActive` | 此部署组正在接收流量（active=true） | 此部署组是后备（active=false） |

`setCondition` 方法确保只在状态真正变化时才更新 `LastTransitionTime`，避免不必要的写操作。

---

## 9. 保留策略（自动过期删除）

### 设计意图

当 blue 切换到 green 后，blue 变成了 `active=false`。一段时间后 blue 旧环境不再需要。保留策略允许自动删除不活跃的部署组。

### 计时器起点

使用 `TrafficActive=False` Condition 的 `LastTransitionTime` 作为倒计时起点：

```go
func getInactiveSince(gs *GameService) time.Time {
    for _, c := range gs.Status.Conditions {
        if c.Type == "TrafficActive" && c.Status == metav1.ConditionFalse {
            return c.LastTransitionTime.Time  // 最后一次变成 inactive 的时刻
        }
    }
    return gs.CreationTimestamp.Time  // 兜底
}
```

### 为什么不用 CreationTimestamp

- `CreationTimestamp` 是 CR 创建时由 K8s 设置的，永不改变
- 如果用 `CreationTimestamp`，blue 和 green 的过期时间会几乎相同（因为它们差不多同时创建）
- 每次蓝绿切换时，`TrafficActive` condition 的 `LastTransitionTime` 会更新，倒计时重新开始

### 保留逻辑

```
if active=false 且 retention.enabled=true:
    duration = ParseDuration(retention.defaultDuration)  // 如 24h
    retentionStart = getInactiveSince()
    
    if retentionStart + duration < now:
        → 删除 GameService CR（触发 finalize 清理所有资源）
    else:
        → requeueAfter = time.Until(retentionStart + duration)
        → 日志: "Retention period active, will auto-delete, requeueAfter: 23h50m"
```

---

## 10. Finalizer 与优雅清理

### Finalizer 是什么

Finalizer 是 Kuberentes 的一种保护机制：带 Finalizer 的 CR 被删除时，只会设置 `DeletionTimestamp`，不会立即从 etcd 移除。只有当所有 Finalizer 被移除后，CR 才真正删除。

### 为什么需要 Finalizer

Operator 创建了 Service 和 Ingress 这些外部资源。如果 CR 被直接删除而没有清理这些资源，它们会成为"孤儿资源"。

### Finalize 流程

```
用户执行 kubectl delete gameservice blue
        │
        ▼
K8s 设置 DeletionTimestamp，不实际上删除（有 Finalizer 保护）
        │
        ▼
Reconcile 检测到 DeletionTimestamp != nil
        │
        ▼
  ┌───────────────────────┐
  │ finalize()            │
  │                       │
  │ 1. DeleteIngress      │  删除 game-ingress-blue
  │                       │
  │ 2. 列出所有 managed   │
  │    Service            │
  │                       │
  │ 3. 逐个 Delete        │  忽略 NotFound（可能已被 GC 删除）
  │                       │
  │ 4. RemoveFinalizer    │  移除 Finalizer
  │                       │
  │ 5. Update(gs)         │  K8s 检测到 Finalizer 已移除，真正删除 CR
  └───────────────────────┘
```

### 添加 Finalizer 的时机

在 CR 第一次被 Reconcile 时（第 59-66 行），如果没有 Finalizer 就添加，然后立即返回。下一次 Reconcile 再执行正常的业务逻辑：

```go
if !controllerutil.ContainsFinalizer(&gs, gameServiceFinalizer) {
    controllerutil.AddFinalizer(&gs, gameServiceFinalizer)
    r.Update(ctx, &gs)
    return ctrl.Result{}, nil  // ← requeue，下次再执行业务逻辑
}
```

---

## 11. 事件驱动机制

### SetupWithManager

这是控制器和 Kubernetes 系统的"接线"：

```go
func (r *GameServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
    // 注册字段索引：通过 connectorNamespace 反向查找 GameService
    mgr.GetFieldIndexer().IndexField(...)

    return ctrl.NewControllerManagedBy(mgr).
        For(&GameService{}).            // 主资源：GameService 变更时触发
        Watches(&Pod{},                 // 次级资源：Pod 变更时触发
            handler.EnqueueRequestsFromMapFunc(r.mapConnectorPodToGameService),
        ).
        Named("gameservice").
        Complete(r)
}
```

### 什么事件会触发 Reconcile

| 事件 | 触发方式 | 说明 |
|------|---------|------|
| GameService CR 创建/修改 | `.For(&GameService{})` | 用户改 YAML |
| connector Pod 创建 | `.Watches(&Pod{})` | StatefulSet 扩容 |
| connector Pod 删除 | `.Watches(&Pod{})` | StatefulSet 缩容 |
| connector Pod 状态变化 | `.Watches(&Pod{})` | Pod 从 Pending → Running |

### mapConnectorPodToGameService

这个函数是 Pod 事件和 GameService 之间的桥梁：

```go
func mapConnectorPodToGameService(ctx, obj) []reconcile.Request {
    pod := obj.(*corev1.Pod)
    if pod.Labels["adventure"] != "connector" {
        return nil  // 不是 connector Pod，忽略
    }
    
    // 通过字段索引查找 connectorNamespace 匹配的所有 GameService
    List(&GameServiceList{}, MatchingFields{"spec.connectorNamespace": pod.Namespace})
    
    // 把找到的 GameService 都加入 reconcile 队列
    return []reconcile.Request{...}
}
```

**关键**：`spec.connectorNamespace` 字段索引是在 `SetupWithManager` 中注册的。这样就能快速从 Pod 所在的 namespace 找到关联的 GameService。

### 不需要 Watch Service 和 Ingress

Service 和 Ingress 的变更（包括被 GC 删除）不触发 Reconcile。因为：
- Service 由 `EnsureService` 在每次 Reconcile 中主动创建/更新
- Orphan Service 由 `DeleteOrphanServices` 主动清理
- Ingress 是同步管理的，不存在外部修改场景

---

## 12. 构建与部署

### 构建流程

```
Dockerfile（多阶段构建）
├── Stage 1: golang:1.25
│   ├── COPY go.mod go.sum → go mod download
│   ├── COPY . → go build -o manager
│   └── 产出: /workspace/manager（静态二进制）
│
└── Stage 2: gcr.io/distroless/static:nonroot
    ├── COPY --from=builder /workspace/manager .
    ├── USER 65532:65532（非 root 运行）
    └── ENTRYPOINT ["/manager"]
```

**为什么用 distroless**：最终镜像只有 20MB 左右，不含 shell、包管理器等，攻击面极小。

### 构建命令

```bash
# 构建镜像（podman 需加 --network host）
docker build --network host -t registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v5 .

# 推送到阿里云容器镜像仓库
docker push registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v5

# 部署到集群（kustomize 自动注入镜像地址）
make deploy IMG=registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v5
```

### Kustomize 部署清单

`make deploy` 等价于：
```bash
cd config/manager && kustomize edit set image controller=${IMG}
kustomize build config/default | kubectl apply -f -
```

`config/default/kustomization.yaml` 聚合的资源：
```
../crd          → CustomResourceDefinition（GameService）
../rbac         → ServiceAccount、Role、RoleBinding、ClusterRole、ClusterRoleBinding
../manager      → Namespace、Deployment
metrics_service.yaml → Service（metrics 端点）
```

### 部署后验证

```bash
kubectl get pods -n gs-operator-system
kubectl logs -n gs-operator-system deployment/gs-operator-controller-manager -c manager -f
kubectl get gameservice -A -o wide
```

### 时区配置

为让日志显示北京时间，在 Deployment 中设置了 `TZ` 环境变量：

```yaml
env:
- name: TZ
  value: Asia/Shanghai
```

效果：日志时间从 `2026-05-08T08:14:09Z` 变为 `2026-05-08T16:14:09+08:00`。

---

## 13. 测试体系

### 单元测试：EnvTest

使用 `controller-runtime/pkg/envtest` 框架，在测试代码中启动一个**真实的 K8s API Server + etcd**：

```go
testEnv = &envtest.Environment{
    CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
}
```

**优势**：无需真实 K8s 集群，纯 Go 测试就能验证 CRUD 行为。

### 测试用例（Ginkgo + Gomega）

| 测试场景 | 验证内容 |
|---------|---------|
| CR 首次创建 | Finalizer 是否被添加 |
| 无 connector Pod 时 | connectorCount=0，Available=True |
| active=true → false 切换 | TrafficActive condition 变为 False |
| GetPodOrdinal | 从 connector-0 提取 ordinal "0" |
| BuildConnectorOrdinals | 去重排序 |

### 测试生命周期

```go
BeforeEach → 准备测试数据
    ↓
It("should...") → 执行被测逻辑
    ↓
AfterEach → 清理 Finalizer + 删除 CR
    ↓
Eventually(...Should(BeTrue())) → 异步等待资源完全删除
```

### 手动测试

`test/manual/README.md` 提供了手动验证流程：
1. 扩容 StatefulSet → 检查 Ingress path 数量
2. 缩容 StatefulSet → 检查 Svc 和 Ingress 自动更新
3. 蓝绿切换 → 验证流量路由变化

---

## 14. 踩坑记录与设计决策

### 决策 1：OwnerReference vs 主动删除

**问题**：缩容时 Service 删除很慢，每次 Reconcile 只删一个。

**分析**：StatefulSet 缩容时 Pod 被标记 `DeletionTimestamp` 但 Phase 仍是 Running。Operator 将其计入 activeOrdinals，导致 Service 不被视为孤儿。

**方案对比**：
- 方案 A：排除 `DeletionTimestamp != nil` 的 Pod → 一次 Reconcile 批量清理
- 方案 B：给 Service 添加 Pod 的 OwnerReference → K8s GC 自动级联删除

**选择**：方案 B（OwnerReference）

**原因**：OwnerReference 是 K8s 原生机制，Service 生命周期和 Pod 严格绑定，不依赖 Operator 主动清理。配合 `DeleteOrphanServices` 作为快速清理兜底。

### 决策 2：保留计时器从 LastTransitionTime 开始

**问题**：蓝绿切换后，保留计时器不重置，blue 和 green 共享相同的过期时间。

**根因**：使用了不可变的 `CreationTimestamp` 作为计时起点。

**修复**：改用 `TrafficActive=False` Condition 的 `LastTransitionTime`。每次切换时 LastTransitionTime 自动更新，计时器重新开始。

### 决策 3：NotFound 不算 ERROR

**问题**：OwnerReference 启用后，GC 可能先于 `DeleteOrphanServices` 删除 Service，导致 `m.Delete()` 返回 NotFound 被当作 ERROR 记录。

**修复**：在 `DeleteOrphanServices` 和 `finalize` 中，`apierrors.IsNotFound(err)` 返回 true 时不报 ERROR，因为 GC 已完成工作。

### 决策 4：并发控制（semaphore vs goroutine pool）

**问题**：串行创建 Service 在 Pod 数量多时性能差。

**选择**：`golang.org/x/sync/semaphore` + `errgroup`，限制并发数为 5。

**原因**：轻量，无需引入 goroutine pool 依赖。5 路并发对 K8s API Server 压力可控。

### 决策 5：不 Watch Service 和 Ingress

**问题**：要不要像 watch Pod 一样 watch Service 和 Ingress 来加速响应？

**选择**：不 Watch。

**原因**：
- Service 由 `EnsureService` 同步管理，不存在外部创建/修改的场景
- Ingress 是 CR 的直接投影，CR 变就重建
- 减少 Watch 数量可以减轻 controller-runtime cache 的内存和事件处理压力

### 决策 6：Leader Election

**原因**：如果 Operator 有多副本（高可用部署），同时只有一个活跃的 leader 执行 Reconcile，其他 standby。防止"脑裂"——多个 controller 同时操作同一资源。

```go
LeaderElectionID: "74afd476.gs.zzrr.io"
```

### 决策 7：RBAC 最小权限

```go
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
```

- Pod：只读（get、list、watch），不修改 Pod
- Service：全权限，创建/更新/删除
- Ingress：全权限，创建/更新/删除

---

## 附录：常用命令速查

```bash
# 代码生成
make manifests    # 从 marker 生成 CRD 和 RBAC
make generate     # 生成 DeepCopy 方法

# 开发
make test         # 运行单元测试（envtest）
make lint-fix     # 自动修复代码风格

# 构建部署
export IMG=registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v5
make docker-build IMG=$IMG   # 构建镜像
make docker-push IMG=$IMG    # 推送镜像
make deploy IMG=$IMG         # 部署到集群

# 查看状态
kubectl get gameservice -A -o wide
kubectl get svc -n gsb -l app.kubernetes.io/managed-by=gs-operator
kubectl get ingress -A -o wide
kubectl logs -n gs-operator-system deployment/gs-operator-controller-manager -c manager -f

# 蓝绿切换
kubectl patch gameservice green -n gsg --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice blue -n gsb --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
```
