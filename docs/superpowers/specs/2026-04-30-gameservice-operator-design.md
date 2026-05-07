# GameService Operator 设计与蓝绿发布方案

**日期：** 2026-04-30
**项目：** gs-operator
**CRD：** GameService (`zzrr.gs.zzrr.io/v1alpha1`)

---

## 1. 背景与目标

当前游戏架构包含三部分服务：
- **前端**（nginx Deployment，adventure 命名空间）
- **登陆服**（gamelogin，Deployment 部署，含 backend 和 logingame）
- **游戏服**（gameserver，含 master、insideapi、webapi、connector）

流量入口为 `Higress → nginx gateway → connector-{id}.connector-svc:3010`。其中 nginx gateway 解析 URL 参数 `?worker_id=` 构造后端地址。

### 目标

1. **去掉 nginx gateway**，改为 Higress Ingress 通过 path 直连 connector pod，减少转发开销
2. **通过 Operator 自动管理** connector pod 对应的 Service 和 Ingress path，支持动态扩缩容
3. **实现游戏服的蓝绿发布**，支持不停服版本更新和快速回滚

## 2. 架构

```
Higress (Ingress Controller)
    │
    ├── /connector0  →  Service: connector-0-svc  →  Pod: connector-0:3010
    ├── /connector1  →  Service: connector-1-svc  →  Pod: connector-1:3010
    ├── /connectorN  →  Service: connector-N-svc  →  Pod: connector-N:3010
    │
    ├── 前端 (Deployment, 滚动更新)
    └── 登陆服 (Deployment, 滚动更新)

游戏服蓝绿发布:
    ├── Namespace: adventure-blue (v1.0, active)
    └── Namespace: adventure-green (v2.0, standby)
```

### 职责边界

| 组件 | 职责 |
|------|------|
| **Operator** | 管理 connector pod 独立 Service、管理 Ingress path 列表、蓝绿 Ingress 切换 |
| **ArgoCD** | 部署游戏服资源（Deployment/StatefulSet/ConfigMap/Namespace）、清理旧版本资源 |
| **用户（手动）** | 触发蓝绿切换（patch `active` 字段）、回滚、提前删除保留版本 |

### 非范围

- Operator 不部署 gameserver 本身的资源（master/insideapi/webapi/connector 的 Deployment/StatefulSet/ConfigMap/Namespace）
- Operator 不管理前端和登陆服的更新（它们用 Deployment 滚动更新）
- 数据库/Redis 架构变更不在处理范围内（大版本更新停服处理）

## 3. CRD 设计

### 完整字段

```yaml
apiVersion: zzrr.gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  name: <blue|green>
spec:
  # --- Ingress 配置 ---
  ingress:
    host: game.example.com
    ingressClassName: higress
    pathType: Prefix
    pathPrefix: "/connector"
    port: 3010
    tls:
      secretName: game-tls
    annotations:                    # 自定义 Ingress 注解，key: value 格式
      nginx.ingress.kubernetes.io/proxy-read-timeout: "300s"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "300s"

  # --- 目标 connector 所在命名空间 ---
  connectorNamespace: adventure-blue

  # --- 蓝绿发布配置 ---
  deployGroup:
    role: blue                # blue | green
    active: true              # true=接收流量, false=不接收

  # --- 保留策略 ---
  retention:
    enabled: true
    defaultDuration: 24h

status:
  conditions:
  - type: Available
    status: "True"
    reason: "AllIngressPathsReady"
    message: "Ingress path configuration is up to date"
  - type: TrafficActive
    status: "True"
    reason: "Active"
  connectorCount: 5
  observedGeneration: 1
```

### TypeScript-style 结构定义

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

type TLSConfig struct {
    SecretName string `json:"secretName"`
}

type DeployGroupConfig struct {
    Role   string `json:"role"`
    Active bool   `json:"active"`
}

type RetentionConfig struct {
    Enabled         bool   `json:"enabled"`
    DefaultDuration string `json:"defaultDuration"`
}
```

## 4. Ingress 动态管理

### Pod-Service 映射

Connector StatefulSet 使用 `podManagementPolicy: Parallel` 和 `updateStrategy: OnDelete`。Operator 通过 Watch connector pod（标签 `adventure=connector`）感知变化：

| 事件 | Operator 响应 |
|------|-------------|
| Pod `connector-i` 创建 | 创建 `Service/connector-i-svc`，selector 为 `statefulset.kubernetes.io/pod-name: connector-i`；patch Ingress 添加 path |
| Pod `connector-i` 删除 | 删除 `Service/connector-i-svc`；patch Ingress 移除 path |
| Pod 重建（同序号） | Service 通过 selector 自动更新 endpoint，Ingress 不需要变更 |

### 每个 Pod 的 Service 定义

```yaml
apiVersion: v1
kind: Service
metadata:
  name: connector-5-svc
  namespace: adventure-blue
  labels:
    app.kubernetes.io/managed-by: gs-operator
spec:
  selector:
    statefulset.kubernetes.io/pod-name: connector-5
  ports:
  - port: 3010
    targetPort: 3010
```

### Ingress 定义

Operator 维护**每个 GameService 角色各一个 Ingress**，但只有 `active: true` 的那个 Ingress 被实际使用。同步过程中所有 path 同时在两个 Ingress 中存在，backend 指向各自 namespace。

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: game-ingress-blue
  labels:
    app.kubernetes.io/managed-by: gs-operator
    gs-role: blue
  annotations:                  # 由 CR spec.ingress.annotations 动态注入
    nginx.ingress.kubernetes.io/proxy-read-timeout: "300s"
spec:
  ingressClassName: higress
  rules:
  - host: game.example.com
    http:
      paths:
      - path: /connector0
        pathType: Prefix
        backend:
          service:
            name: connector-0-svc
            port:
              number: 3010
      - path: /connector1
        ...
```

跨 namespace 引用 Service 通过 Higress 的 McpBridge 或自定义注解实现。具体实现时确认 Higress 版本的支持能力。

## 5. 蓝绿发布

### 初始状态

```yaml
# GameService/blue（长期存在）
spec:
  connectorNamespace: adventure-blue
  deployGroup:
    role: blue
    active: true
```

`adventure-blue` 运行 v1.0，Ingress 指向 `adventure-blue` 下的 connector Service。

### 准备新版本

```yaml
# GameService/green（部署新版本时创建）
spec:
  connectorNamespace: adventure-green
  deployGroup:
    role: green
    active: false
```

Operator 发现 `adventure-green` 中的 connector pod，自动创建 Service 并同步 Ingress path。但 Ingress backend 仍指向 `adventure-blue`。用户通过 ArgoCD 在 `adventure-green` 部署 v2.0。

### 切换

```bash
kubectl patch gameservice green --type merge \
  -p '{"spec":{"deployGroup":{"active":true}}}'

kubectl patch gameservice blue --type merge \
  -p '{"spec":{"deployGroup":{"active":false}}}'
```

**切换期间的间隙处理：** 两个 CR 都 `active: true` 时，Operator 不拒绝，保持当前 Ingress 不变，等待下一次 Reconcile（第二个 patch 触发）解决状态。

切换后 Operator 将 Ingress 所有 path 的 backend 指向 `adventure-green`。新玩家连接进入 v2.0。

### 回滚

```bash
kubectl patch gameservice blue --type merge \
  -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice green --type merge \
  -p '{"spec":{"deployGroup":{"active":false}}}'
```

Ingress 切回 `adventure-blue`。

### 保留与清理

| 条件 | 行为 |
|------|------|
| GameService/blue `active: false` 且 `retention.enabled: true` | 开始 24h 保留倒计时 |
| 24h 内回滚 | 重置倒计时 |
| 24h 到期 | Operator 自动删除 GameService/blue 及对应 Service |
| 用户提前手动删除 | `kubectl delete gameservice blue` |

## 6. Reconciliation 逻辑

```
触发条件:
  - GameService CR 变更
  - Connector Pod 变化 (Watch 标签 adventure=connector)

Reconcile 流程:
  1. 获取 GameService 实例
  2. 获取 connectorNamespace 中所有 connector pod
  3. 确保每个 pod 的独立 Service 存在（不存在则创建）
  4. 构建 Ingress path 列表，与现有 Ingress 对比，不一致则 patch
  5. 根据 deployGroup.active 确定 Ingress backend namespace:
     - active=true:  backend → 当前 namespace
     - active=false: 检查对端 CR，如有活跃 CR 则指向对端 namespace
  6. 处理保留期逻辑（非活跃且 retention.enabled）
  7. 更新 Status (conditions, connectorCount, observedGeneration)

两 CR 同时 active=true 的处理:
  - 不拒绝，记录 Info 日志
  - 保持当前 Ingress 不做切换
  - 等待下一次 Reconcile 由第二个 patch 解决
```

## 7. 错误处理

| 场景 | 处理 |
|------|------|
| Service 创建冲突 | 跳过，视为已存在 |
| Ingress patch 失败 | 记录 Error 事件，Status 设为 Degraded，重试 |
| Connector pod 列表为空 | 更新 Status 不报错，等待 pod 创建 |
| 对端 CR 不存在且 active=false | Ingress 指向自身（兜底） |
| 保留期资源删除失败 | 记录 Error 事件，下次 Reconcile 重试 |

## 8. 测试策略

- **单元测试**：Reconcile 核心逻辑（Service 创建、Ingress path 同步、active 状态判断）
- **envtest 集成测试**：创建 GameService CR → 模拟 pod 创建 → 验证 Service/Ingress 被正确创建
- **E2E（Kind 集群）**：完整流程 — 部署 connector StatefulSet → 创建 CR → Service/Ingress 自动创建 → 蓝绿切换 → 回滚 → 保留期自动清理

## 9. 实现注意事项

- Connector StatefulSet 的 `serviceName: connector-svc` headless service 需要保留（用于 pod DNS 解析）
- Ingress 跨 namespace 路由需确认 Higress 版本对 `externalName` Service 或 McpBridge 的支持
- WebSocket 支持通过 Ingress 的以下配置保证：`nginx.ingress.kubernetes.io/proxy-read-timeout: 300s` 等 Higress 对应注解
- Pod selector 使用 `statefulset.kubernetes.io/pod-name` 标签，这是 K8s StatefulSet 自动注入的标准标签
