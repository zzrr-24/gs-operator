# Gateway API 适配流程说明

> 本文梳理 `gs-operator` 本次适配 Gateway API 的代码思路、包引入原因、关键函数职责和完整业务流程。当前实现目标是兼容现有 Ingress 版本，同时让新云上集群可以通过 Gateway API 的 `HTTPRoute` 承载流量入口。

---

## 1. 适配目标

原版本只管理 Kubernetes 标准 `Ingress`：

```
GameService
  ├── connector Pod 列表
  ├── 每个 Pod 一个 Service
  └── active=true 时维护 game-ingress-{role}
```

本次适配后，入口资源由 `spec.trafficMode` 显式选择：

```
trafficMode 缺省或 Ingress
  └── 继续维护 game-ingress-{role}

trafficMode = Gateway
  └── 维护 game-route-{role} HTTPRoute
```

核心原则：

- `trafficMode` 缺省时默认走 Ingress；已有 CR 需要把公共路由字段迁移到 `spec.route`。
- Gateway 模式只管理 `HTTPRoute`，不创建 `Gateway` 或 `GatewayClass`。
- `HTTPRoute` 创建在 `connectorNamespace`，和后端 Service 同 namespace，避免跨 namespace backend 引用。
- `spec.route` 保存 Ingress 和 Gateway API 共用的 `host/pathType/pathPrefix/port`。
- `spec.ingress` 只保存 Ingress 专属配置，Gateway 模式不需要填写。
- Gateway 模式不迁移 `ingress.annotations` 和 `ingress.tls`，这些仍只对 Ingress 模式生效。

---

## 2. API 字段设计

代码位置：`api/v1alpha1/gameservice_types.go`

### 2.1 `RouteConfig`

```go
type RouteConfig struct {
    Host       string `json:"host"`
    PathType   string `json:"pathType"`
    PathPrefix string `json:"pathPrefix"`
    Port       int32  `json:"port"`
}
```

`spec.route` 是 Ingress 和 Gateway API 共用的必填路由配置：

- `host`：Ingress rule host 或 HTTPRoute hostname。
- `pathType`：Ingress pathType 或 HTTPRoute path match type。
- `pathPrefix`：每个 connector 路径的公共前缀。
- `port`：connector Service 端口和入口 backend 端口。

公共字段从 `spec.ingress` 抽离后，`spec.ingress` 只负责 Ingress 专属配置：

```go
type IngressConfig struct {
    IngressClassName string            `json:"ingressClassName"`
    TLS              *TLSConfig        `json:"tls,omitempty"`
    Annotations      map[string]string `json:"annotations,omitempty"`
}
```

这次调整会改变 CR YAML 结构，已有对象或部署清单需要按下面方式迁移：

```yaml
# 调整前
spec:
  ingress:
    host: game.example.com
    pathType: Prefix
    pathPrefix: /connector
    port: 3010
    ingressClassName: higress

# 调整后
spec:
  route:
    host: game.example.com
    pathType: Prefix
    pathPrefix: /connector
    port: 3010
  ingress:
    ingressClassName: higress
```

Gateway 模式只需要 `route + gateway`，不需要提供 `ingress`。

为避免升级过程中旧对象被读取后使用空路由参数，控制器在创建 Service 或入口资源前调用 `validateRouteConfig`。缺少 `spec.route` 时会记录 `InvalidRouteConfig` Event 并停止本次调和，直到用户完成 CR 迁移。

### 2.2 `TrafficMode`

```go
// +kubebuilder:validation:Enum=Ingress;Gateway
type TrafficMode string

const (
    TrafficModeIngress TrafficMode = "Ingress"
    TrafficModeGateway TrafficMode = "Gateway"
)
```

`TrafficMode` 是入口资源类型开关：

- 空值：按 `Ingress` 处理，兼容未设置 `trafficMode` 的 CR。
- `Ingress`：创建/更新 `networking.k8s.io/v1 Ingress`。
- `Gateway`：创建/更新 `gateway.networking.k8s.io/v1 HTTPRoute`。

字段定义：

```go
// +kubebuilder:default=Ingress
TrafficMode TrafficMode `json:"trafficMode,omitempty"`
```

这里设置 `default=Ingress`，让新建 CR 在 API Server 默认化后也明确表现为 Ingress 模式。但控制器里仍通过 `effectiveTrafficMode` 处理空值，保证旧对象和测试对象也稳定兼容。

### 2.3 `GatewayConfig`

```go
type GatewayConfig struct {
    // +kubebuilder:validation:Required
    ParentRef GatewayParentRef `json:"parentRef"`
}

type GatewayParentRef struct {
    Name        string `json:"name"`
    Namespace   string `json:"namespace,omitempty"`
    SectionName string `json:"sectionName,omitempty"`
}
```

这部分对应 Gateway API `HTTPRoute.spec.parentRefs`：

- `name`：目标 Gateway 名称，Gateway 模式必填。
- `namespace`：目标 Gateway namespace，可选；不填表示和 HTTPRoute 同 namespace。
- `sectionName`：目标 Gateway listener 名称，可选，用于绑定某个 listener。

### 2.4 CRD 校验

```go
// +kubebuilder:validation:XValidation:rule="has(self.trafficMode) && self.trafficMode == 'Gateway' || has(self.ingress)",message="ingress is required when trafficMode is Ingress"
// +kubebuilder:validation:XValidation:rule="!has(self.trafficMode) || self.trafficMode != 'Gateway' || has(self.gateway)",message="gateway is required when trafficMode is Gateway"
```

校验逻辑：

- `trafficMode` 缺省时允许通过，保持旧对象兼容。
- `route` 在所有入口模式下都是必填公共配置。
- `trafficMode` 缺省或为 `Ingress` 时必须填写 `ingress`。
- `trafficMode=Gateway` 时必须填写 `gateway`。

`ingress` 在 `GameServiceSpec` 中改为可选指针：

```go
Ingress *IngressConfig `json:"ingress,omitempty"`
```

原因是 Gateway 模式不使用任何 Ingress 专属字段，不应要求填写空的 `ingress` 对象。

---

## 3. 引入包及作用

### 3.1 `sigs.k8s.io/gateway-api/apis/v1`

使用位置：

- `cmd/main.go`
- `internal/controller/suite_test.go`
- `internal/controller/httproute_manager.go`
- `internal/controller/gameservice_controller_test.go`

作用：

- 提供 Gateway API v1 类型，包括 `HTTPRoute`、`HTTPRouteSpec`、`ParentReference`、`HTTPRouteRule`、`HTTPRouteMatch`、`HTTPBackendRef`。
- 提供 `Install`，让 controller-runtime client 能读写 Gateway API 对象。

Manager 启动时注册：

```go
utilruntime.Must(gatewayv1.Install(scheme))
```

如果不注册，`client.Create(ctx, &gatewayv1.HTTPRoute{})` 会因为 scheme 不认识该 GVK 而失败。

### 3.2 `k8s.io/apimachinery/pkg/api/errors`

使用位置：

- `internal/controller/httproute_manager.go`
- `internal/controller/ingress_manager.go`

作用：

- 使用 `apierrors.IsNotFound(err)` 区分“资源不存在”和“其他 API 错误”。
- 只有资源不存在时才进入 `Create` 分支。
- 如果是 RBAC、API Server 临时故障、网络错误等，直接返回错误，避免误判为需要创建。

### 3.3 `k8s.io/apimachinery/pkg/api/meta`

使用位置：

- `internal/controller/httproute_manager.go`
- `internal/controller/gameservice_controller_test.go`

作用：

- 使用 `apimeta.IsNoMatchError(err)` 识别集群未安装 HTTPRoute CRD 的场景。
- 只在 `DeleteHTTPRoute` 清理路径中把 `NoMatch` 视为成功，保证纯 Ingress 集群无需安装 Gateway API CRD。

Ingress 和 HTTPRoute 当前都采用同样逻辑：

```go
if err := m.Get(ctx, key, &existing); err != nil {
    if !apierrors.IsNotFound(err) {
        return fmt.Errorf("failed to get ...: %w", err)
    }
    return m.Create(ctx, desired)
}
```

### 3.3 `maps`

使用位置：

- `ingress_manager.go`
- `httproute_manager.go`

作用：

- 更新已有资源时复制 operator 管理的 labels。
- Ingress 版本还复制 annotations；HTTPRoute 版本不复制 annotations，因为 Gateway 模式明确不迁移 Ingress annotations。

---

## 4. HTTPRouteManager 代码思路

代码位置：`internal/controller/httproute_manager.go`

这个文件的风格刻意对齐 `IngressManager`：

- struct 同样嵌入 `client.Client` 和 `Scheme`。
- reconcile 函数同样先构建 desired 对象，再 Get/Create/Update。
- ownerReference 规则同样只在 CR 和入口资源同 namespace 时设置。
- 跨 namespace 时依赖 labels 管理。

### 4.1 `HTTPRouteManager`

```go
type HTTPRouteManager struct {
    client.Client
    Scheme *runtime.Scheme
}
```

职责：

- 使用 controller-runtime client 读写 `HTTPRoute`。
- 使用 `Scheme` 为同 namespace 的 `GameService -> HTTPRoute` 设置 ownerReference。

### 4.2 `NewHTTPRouteManager`

```go
func NewHTTPRouteManager(c client.Client, s *runtime.Scheme) *HTTPRouteManager {
    return &HTTPRouteManager{Client: c, Scheme: s}
}
```

和 `NewIngressManager` 保持一致。控制器每次 Reconcile 时基于当前 client/scheme 创建 manager。

### 4.3 `ReconcileHTTPRoute`

函数签名：

```go
func (m *HTTPRouteManager) ReconcileHTTPRoute(ctx context.Context, gs *zzrrv1alpha1.GameService, ordinals []string) error
```

入参含义：

- `ctx`：本次调和上下文。
- `gs`：当前 `GameService`。
- `ordinals`：从 connector Pod 名称中提取出的 ordinal 列表，例如 `["0", "1", "2"]`。

执行步骤：

1. 没有 connector Pod 时跳过 HTTPRoute reconcile。

   ```go
   if len(ordinals) == 0 {
       log.Info("No connector pods, skipping HTTPRoute reconcile")
       return nil
   }
   ```

   这和 Ingress 版本一致。没有 Pod 时不创建空入口。

2. Gateway 模式必须有 `spec.gateway`。

   ```go
   if gs.Spec.Gateway == nil {
       return fmt.Errorf("gateway config is required when trafficMode is Gateway")
   }
   ```

   CRD 层面已经校验，这里是控制器防御式检查。

3. 生成 HTTPRoute 名称。

   ```go
   httpRouteName := fmt.Sprintf("game-route-%s", gs.Spec.DeployGroup.Role)
   ```

   命名规则：

   - blue 组：`game-route-blue`
   - green 组：`game-route-green`

   对应 Ingress 版本的 `game-ingress-{role}`。

4. 生成路径匹配类型。

   ```go
   pathType := gatewayv1.PathMatchPathPrefix
   if gs.Spec.Route.PathType == "Exact" {
       pathType = gatewayv1.PathMatchExact
   }
   ```

   Gateway API 标准支持 `PathPrefix` 和 `Exact`。当前实现把 Ingress 的：

   - `Prefix` 映射成 `PathPrefix`
   - `Exact` 映射成 `Exact`
   - `ImplementationSpecific` 映射成 `PathPrefix`

   这样做是为了保持 Gateway 模式尽量使用标准语义。Gateway API 没有和 Ingress `ImplementationSpecific` 完全等价的通用行为。

5. 为每个 connector ordinal 生成一条 `HTTPRouteRule`。

   例如 ordinal 是 `0`：

   ```go
   path := fmt.Sprintf("%s%s", gs.Spec.Route.PathPrefix, ord)
   svcName := gatewayv1.ObjectName(fmt.Sprintf("connector-%s-svc", ord))
   port := gs.Spec.Route.Port
   ```

   生成结果：

   ```yaml
   rules:
   - matches:
     - path:
         type: PathPrefix
         value: /connector0
     backendRefs:
     - name: connector-0-svc
       port: 3010
   ```

6. 构建 `parentRef`。

   ```go
   parentRef := gatewayv1.ParentReference{
       Name: gatewayv1.ObjectName(gs.Spec.Gateway.ParentRef.Name),
   }
   ```

   如果 CR 填了 namespace 和 sectionName，则继续填入：

   ```go
   parentRef.Namespace = &namespace
   parentRef.SectionName = &sectionName
   ```

   这个 parentRef 告诉 Gateway controller：这个 HTTPRoute 要挂到哪个 Gateway 或哪个 listener 上。

7. 构建 desired HTTPRoute。

   ```go
   desiredHTTPRoute := &gatewayv1.HTTPRoute{
       ObjectMeta: metav1.ObjectMeta{
           Name:      httpRouteName,
           Namespace: gs.Spec.ConnectorNamespace,
           Labels: map[string]string{
               "app.kubernetes.io/managed-by": "gs-operator",
               "gs-role":                      gs.Spec.DeployGroup.Role,
           },
       },
       Spec: gatewayv1.HTTPRouteSpec{
           CommonRouteSpec: gatewayv1.CommonRouteSpec{
               ParentRefs: []gatewayv1.ParentReference{parentRef},
           },
           Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(gs.Spec.Route.Host)},
           Rules:     rules,
       },
   }
   ```

   注意：

   - namespace 是 `connectorNamespace`。
   - hostname 读取公共配置 `route.host`。
   - backendRefs 不填 namespace，因为 Service 和 HTTPRoute 在同一个 namespace。
   - 不设置 annotations。
   - 不设置 TLS；TLS 由 Gateway listener 侧配置。

8. 设置 ownerReference。

   ```go
   if gs.Namespace == gs.Spec.ConnectorNamespace {
       controllerutil.SetControllerReference(gs, desiredHTTPRoute, m.Scheme)
   }
   ```

   Kubernetes 不允许跨 namespace ownerReference。逻辑和 Ingress 版本一致：

   - CR 和 HTTPRoute 同 namespace：设置 ownerReference，可被 GC。
   - CR 和 HTTPRoute 不同 namespace：不设置 ownerReference，依赖 labels 和 finalizer 清理。

9. Get/Create/Update。

   ```go
   var existingHTTPRoute gatewayv1.HTTPRoute
   if err := m.Get(ctx, key, &existingHTTPRoute); err != nil {
       if !apierrors.IsNotFound(err) {
           return fmt.Errorf("failed to get httproute: %w", err)
       }
       return m.Create(ctx, desiredHTTPRoute)
   }

   existingHTTPRoute.Spec = desiredHTTPRoute.Spec
   maps.Copy(existingHTTPRoute.Labels, desiredHTTPRoute.Labels)
   return m.Update(ctx, &existingHTTPRoute)
   ```

   行为：

   - 不存在：创建。
   - 已存在：覆盖 spec，更新 operator 管理 labels。
   - 其他读取错误：返回错误，不误创建。

### 4.4 `DeleteHTTPRoute`

```go
func (m *HTTPRouteManager) DeleteHTTPRoute(ctx context.Context, gs *zzrrv1alpha1.GameService) error
```

职责：

- 按 `game-route-{role}` 查找 HTTPRoute。
- HTTPRoute 不存在时忽略。
- 集群未安装 Gateway API CRD、API Server 返回 `NoMatch` 时忽略。
- 找到时删除。

使用场景：

- `trafficMode=Ingress` 时清理旧 HTTPRoute。
- `deployGroup.active=false` 时清理备用组 HTTPRoute。
- GameService 删除进入 finalizer 时清理 HTTPRoute。

`NoMatch` 只在删除清理时按“没有可清理资源”处理。Gateway 模式创建或更新 HTTPRoute 时，如果集群没有安装 Gateway API CRD，错误仍会返回给 Reconcile。这样既保证纯 Ingress 集群不依赖 Gateway API CRD，又避免 Gateway 模式配置错误被静默忽略。

---

## 5. Reconcile 中的完整业务流程

代码位置：`internal/controller/gameservice_controller.go`

### 5.1 初始化 manager

```go
svcMgr := NewConnectorServiceManager(r.Client, r.Scheme)
ingMgr := NewIngressManager(r.Client, r.Scheme)
routeMgr := NewHTTPRouteManager(r.Client, r.Scheme)
```

三个 manager 分工：

| manager | 职责 |
|---|---|
| `ConnectorServiceManager` | 为每个 connector Pod 创建/更新 Service，清理孤儿 Service |
| `IngressManager` | Ingress 模式下维护 `game-ingress-{role}` |
| `HTTPRouteManager` | Gateway 模式下维护 `game-route-{role}` |

### 5.2 获取 connector Pod 并生成 ordinal

在获取 Pod 之前，控制器先校验公共路由配置：

```go
if err := validateRouteConfig(gs.Spec.Route); err != nil {
    log.Error(err, "Invalid route config")
    r.Recorder.Event(&gs, corev1.EventTypeWarning, "InvalidRouteConfig", err.Error())
    return ctrl.Result{}, err
}
```

这个运行时保护主要用于 CRD 升级期间的已有旧对象。新创建或更新的对象由 CRD schema 强制要求 `spec.route`。

```go
pods, err := svcMgr.ListConnectorPods(...)
servingPods := filterServiceableConnectorPods(pods)
ordinals := ingMgr.BuildConnectorOrdinals(podNames)
```

流程：

1. 根据 `connectorNamespace`、`podLabelKey`、`podLabelValue` 找 connector Pod。
2. 只保留 Running/Pending Pod。
3. 从 Pod 名称中提取 ordinal。

例如：

| Pod 名称 | ordinal | Service 名称 | 路径 |
|---|---|---|---|
| `connector-0` | `0` | `connector-0-svc` | `/connector0` |
| `connector-1` | `1` | `connector-1-svc` | `/connector1` |

### 5.3 先确保 Service，再处理入口

```go
if _, err := svcMgr.EnsureService(egCtx, &pod, gs.Spec.Route.Port); err != nil {
    return err
}
```

入口资源无论是 Ingress 还是 HTTPRoute，最终 backend 都指向 Service。因此 Reconcile 先确保每个 connector Pod 对应的 Service 存在。

如果 Service 创建失败，控制器直接返回错误，不继续创建入口。这能避免生成指向不存在 Service 的 Ingress/HTTPRoute。

### 5.4 active=true：根据 trafficMode 选择入口资源

```go
if gs.Spec.DeployGroup.Active {
    switch effectiveTrafficMode(&gs) {
    case zzrrv1alpha1.TrafficModeGateway:
        ...
    default:
        ...
    }
}
```

`effectiveTrafficMode`：

```go
func effectiveTrafficMode(gs *zzrrv1alpha1.GameService) zzrrv1alpha1.TrafficMode {
    if gs.Spec.TrafficMode == "" {
        return zzrrv1alpha1.TrafficModeIngress
    }
    return gs.Spec.TrafficMode
}
```

作用：

- 兼容未设置 `trafficMode` 的 CR。
- 兼容测试或旧对象中没有被 API Server default 的情况。

#### Gateway 模式

```go
if err := ingMgr.DeleteIngress(ctx, &gs); err != nil {
    ...
}
if err := routeMgr.ReconcileHTTPRoute(ctx, &gs, ordinals); err != nil {
    ...
}
```

业务含义：

1. 先删除同 role 的旧 Ingress。
2. 再创建或更新 HTTPRoute。

为什么要先删 Ingress：

- 防止 CR 从 Ingress 模式切到 Gateway 模式时，旧 Ingress 继续暴露流量。
- 保证同一个 GameService 当前只有一种入口资源承载流量。

状态更新：

```go
r.setCondition(&gs, "Available", metav1.ConditionTrue, "AllHTTPRoutePathsReady",
    fmt.Sprintf("HTTPRoute paths synced for %d connector pods", len(ordinals)))
```

#### Ingress 模式

```go
if err := routeMgr.DeleteHTTPRoute(ctx, &gs); err != nil {
    ...
}
if err := ingMgr.ReconcileIngress(ctx, &gs, ordinals); err != nil {
    ...
}
```

业务含义：

1. 先删除同 role 的旧 HTTPRoute。
2. 再创建或更新 Ingress。

这和 Gateway 模式互为镜像，保证模式切换时不会留下旧入口。

状态更新：

```go
r.setCondition(&gs, "Available", metav1.ConditionTrue, "AllIngressPathsReady",
    fmt.Sprintf("Ingress paths synced for %d connector pods", len(ordinals)))
```

### 5.5 active=false：备用组清理所有入口

```go
if err := ingMgr.DeleteIngress(ctx, &gs); err != nil {
    ...
}
if err := routeMgr.DeleteHTTPRoute(ctx, &gs); err != nil {
    ...
}
```

备用组不应该接收流量。因此无论当前 `trafficMode` 是什么，都删除两类入口：

- 删除 Ingress，防止旧 Ingress 残留。
- 删除 HTTPRoute，防止旧 HTTPRoute 残留。

状态更新：

```go
Available=True, Reason=Standby
TrafficActive=False, Reason=Standby
```

### 5.6 finalizer：删除 CR 时清理入口

```go
if err := ingMgr.DeleteIngress(ctx, gs); err != nil {
    return ctrl.Result{}, err
}
if err := routeMgr.DeleteHTTPRoute(ctx, gs); err != nil {
    return ctrl.Result{}, err
}
```

删除 GameService 时，同样同时清理 Ingress 和 HTTPRoute。原因是用户可能在 CR 生命周期中切换过 `trafficMode`，finalizer 不能只按当前模式清理，否则旧资源可能残留。

---

## 6. 资源生成示例

Gateway 模式 sample：

```yaml
apiVersion: gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  name: blue-gateway
spec:
  trafficMode: Gateway
  route:
    host: game.example.com
    pathType: Prefix
    pathPrefix: "/connector"
    port: 3010
  gateway:
    parentRef:
      name: shared-gateway
      namespace: gateway-system
      sectionName: http
  connectorNamespace: adventure
  podLabelKey: app
  podLabelValue: connector
  deployGroup:
    role: blue
    active: true
```

当存在 `connector-0` 和 `connector-1` 两个 Pod 时，Operator 生成的 HTTPRoute 形态大致如下：

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: game-route-blue
  namespace: adventure
  labels:
    app.kubernetes.io/managed-by: gs-operator
    gs-role: blue
spec:
  parentRefs:
  - name: shared-gateway
    namespace: gateway-system
    sectionName: http
  hostnames:
  - game.example.com
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /connector0
    backendRefs:
    - name: connector-0-svc
      port: 3010
  - matches:
    - path:
        type: PathPrefix
        value: /connector1
    backendRefs:
    - name: connector-1-svc
      port: 3010
```

---

## 7. Scheme、RBAC、CRD 和测试

### 7.1 Scheme 注册

`cmd/main.go`：

```go
utilruntime.Must(gatewayv1.Install(scheme))
```

`internal/controller/suite_test.go`：

```go
err = gatewayv1.Install(scheme.Scheme)
Expect(err).NotTo(HaveOccurred())
```

生产和测试都必须注册 Gateway API scheme。`Install` 是 Gateway API 当前版本提供的推荐注册入口，替代已废弃的 `AddToScheme`。

### 7.2 RBAC

`internal/controller/gameservice_controller.go` 增加 marker：

```go
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
```

运行 `make manifests` 后生成到 `config/rbac/role.yaml`。

### 7.3 CRD 生成

运行：

```bash
make manifests generate
```

会更新：

- `config/crd/bases/gs.zzrr.io_gameservices.yaml`
- `config/rbac/role.yaml`
- `api/v1alpha1/zz_generated.deepcopy.go`

这些生成文件不手写，统一由 `controller-gen` 根据 Go 类型和 markers 生成。

### 7.4 envtest 加载 Gateway API CRD

`suite_test.go` 增加：

```go
CRDDirectoryPaths: []string{
    filepath.Join("..", "..", "config", "crd", "bases"),
    gatewayAPICRDPath(),
}
```

`gatewayAPICRDPath()` 从 Go module cache 中定位：

```text
sigs.k8s.io/gateway-api@v1.5.1/config/crd/standard
```

原因是 envtest 启动的是临时 API Server，它默认只认识测试加载进去的 CRD。如果不加载 Gateway API 标准 CRD，测试里创建 HTTPRoute 会失败。

### 7.5 单元测试覆盖

`internal/controller/gameservice_controller_test.go` 增加场景：

- `trafficMode` 缺省时仍创建 Ingress。
- `trafficMode=Gateway` 且 active=true 时创建 HTTPRoute。
- 校验 HTTPRoute 的 parentRefs、hostnames、path match、backendRefs。
- Gateway 模式不复制 `ingress.annotations` 到 HTTPRoute。
- active 从 true 切到 false 后删除 HTTPRoute。
- 集群未安装 Gateway API CRD 时，Ingress 模式清理 HTTPRoute 忽略 `NoMatch`，继续创建 Ingress。

### 7.6 无 Gateway API CRD 的真实集群兼容验收

当前测试集群未安装 `httproutes.gateway.networking.k8s.io` CRD。只安装 GameService CRD 并启动当前 controller 后，使用 `test/manual` 验证：

- Ingress 模式能够正常为 connector Pod 创建独立 Service。
- 每个 Service 的 EndpointSlice 只包含对应 ordinal Pod。
- active 组创建 Ingress，standby 组不保留 Ingress。
- active 在 blue/green 之间切换时，旧组 Ingress 删除，新组 Ingress 创建。
- 同一个 `Host: game.example.com` 和 `/connector0` 路径通过 Higress 访问时，返回值按 blue → green → blue 切换。

本次验收同时暴露并修复了删除 HTTPRoute 时未忽略 `NoMatch` 的问题。修复后，纯 Ingress 集群不需要预装 Gateway API CRD。

---

## 8. 完整业务时序

### 8.1 Ingress 模式

```
用户创建/更新 GameService
  │
  ├── Reconcile 读取 GameService
  ├── 添加 finalizer
  ├── 列出 connector Pod
  ├── 过滤 Running/Pending Pod
  ├── 提取 ordinals
  ├── 为每个 Pod 确保 connector-{ordinal}-svc
  ├── 清理孤儿 Service
  ├── trafficMode 缺省或 Ingress
  │     ├── 删除 stale HTTPRoute
  │     └── ReconcileIngress 创建/更新 game-ingress-{role}
  └── 更新 status
```

### 8.2 Gateway 模式

```
用户创建/更新 GameService
  │
  ├── Reconcile 读取 GameService
  ├── 添加 finalizer
  ├── 列出 connector Pod
  ├── 过滤 Running/Pending Pod
  ├── 提取 ordinals
  ├── 为每个 Pod 确保 connector-{ordinal}-svc
  ├── 清理孤儿 Service
  ├── trafficMode = Gateway
  │     ├── 删除 stale Ingress
  │     └── ReconcileHTTPRoute 创建/更新 game-route-{role}
  └── 更新 status
```

### 8.3 蓝绿切换

```
blue active=true
green active=false
  │
  ├── blue 维护入口资源
  └── green 删除入口资源

切换到 green
  │
  ├── green active=true
  │     └── 创建 green 对应入口资源
  └── blue active=false
        └── 删除 blue 对应入口资源
```

这里的“入口资源”由 `trafficMode` 决定：

- Ingress 模式：`game-ingress-blue/green`
- Gateway 模式：`game-route-blue/green`

### 8.4 删除 GameService

```
用户删除 GameService
  │
  ├── Reconcile 发现 deletionTimestamp
  ├── DeleteIngress
  ├── DeleteHTTPRoute
  ├── 删除 managed Service
  ├── 移除 finalizer
  └── Kubernetes 完成 CR 删除
```

同时删 Ingress 和 HTTPRoute 是为了覆盖“CR 生命周期中曾经切换过 trafficMode”的场景。

---

## 9. 当前实现边界

当前实现不做这些事情：

- 不安装 Gateway API CRD。
- 不创建 Gateway controller。
- 不创建 `Gateway`。
- 不创建 `GatewayClass`。
- 不创建 `ReferenceGrant`。
- 不把 Ingress annotations 翻译成 Gateway filters。
- 不把 Ingress TLS 配置迁移到 HTTPRoute。

Ingress 模式可以运行在未安装 Gateway API CRD 的集群中。只有选择 `trafficMode=Gateway` 时，平台才必须预先安装 Gateway API CRD。

这些边界对应当前平台约定：

- 云上平台负责安装 Gateway API 和 Gateway controller。
- 平台预先提供 `Gateway/GatewayClass`。
- 平台配置 Gateway listener、TLS、allowedRoutes。
- Operator 只负责把每个 GameService active 组投影成 Ingress 或 HTTPRoute。

---

## 10. 验证命令

本次适配的代码测试不依赖真实集群 Gateway controller，只依赖 envtest：

```bash
GOTOOLCHAIN=go1.25.10 \
GOCACHE=/private/tmp/gs-operator-gocache \
make manifests generate
```

```bash
HTTPS_PROXY=http://127.0.0.1:7897 \
HTTP_PROXY=http://127.0.0.1:7897 \
GOTOOLCHAIN=go1.25.10 \
GOCACHE=/private/tmp/gs-operator-gocache \
make test
```

说明：

- 本机默认 `go1.25.3` 工具链缺少 coverage 所需的 `covdata`，完整 `make test` 使用 `GOTOOLCHAIN=go1.25.10`。
- 如果下载依赖遇到网络问题，使用代理 `127.0.0.1:7897`。
- Ingress 兼容和蓝绿切换已在未安装 Gateway API CRD 的真实测试集群完成验证。
- Gateway 模式集群内联调仍需等真实集群安装 Gateway API CRD 和 Gateway controller 后再执行。

---

## 11. 后续集群内验证建议

Gateway controller 安装后，建议按下面顺序验证：

1. 确认 Gateway API CRD 已安装：

   ```bash
   kubectl get crd httproutes.gateway.networking.k8s.io
   ```

2. 确认平台 Gateway 已存在：

   ```bash
   kubectl get gateway -A
   kubectl get gatewayclass
   ```

3. 应用 Gateway sample：

   ```bash
   kubectl apply -f config/samples/zzrr_v1alpha1_gameservice_gateway.yaml
   ```

4. 查看 HTTPRoute：

   ```bash
   kubectl get httproute -n adventure
   kubectl describe httproute game-route-blue -n adventure
   ```

5. 检查 parent attachment 状态：

   ```bash
   kubectl get httproute game-route-blue -n adventure -o yaml
   ```

6. 验证路径访问：

   ```bash
   curl -H "Host: game.example.com" http://<gateway-address>/connector0
   ```

7. 验证蓝绿切换：

   ```bash
   kubectl patch gameservice green --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
   kubectl patch gameservice blue --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
   kubectl get httproute -A
   ```

预期：

- active 组存在 `game-route-{role}`。
- standby 组没有 `HTTPRoute`。
- 切换后旧 active 组的入口资源被删除。
