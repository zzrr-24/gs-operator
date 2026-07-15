# gs-operator

游戏服 Kubernetes Operator — 自动管理 Connector Pod 的 Service 和 Ingress/HTTPRoute，支持蓝绿发布。

## 概述

gs-operator 是一个基于 Kubebuilder 的 Kubernetes Operator，用于管理游戏服 connector 的流量入口和版本更新。

### 解决的问题

- **去 Nginx Gateway**：通过 Ingress Path 直连 Connector Pod，减少网络跳转
- **动态入口管理**：Connector Pod 扩缩容时自动增删 Service 和 Ingress Path 或 HTTPRoute Rule
- **Gateway API 兼容**：旧集群默认继续使用 Ingress，新云上环境可显式切换到 Gateway API HTTPRoute
- **蓝绿发布**：通过两个 GameService CR 管理两套游戏服环境，手动切换入口流量

### 架构

```
Higress (Ingress Controller) or Gateway API Gateway
    │
    ├── /connector0  →  Service: connector-0-svc  →  Pod: connector-0
    ├── /connector1  →  Service: connector-1-svc  →  Pod: connector-1
    ├── /connector2  →  Service: connector-2-svc  →  Pod: connector-2
    └── ...
```

### 职责边界

| 组件 | 职责 |
|------|------|
| **Operator** | 管理 Connector Pod 的 Service、Ingress/HTTPRoute、蓝绿切换 |
| **ArgoCD / 用户** | 部署游戏服本体（Deployment/StatefulSet/ConfigMap/Namespace）、清理旧版本 |
| **用户（手动）** | 触发蓝绿切换（patch `active` 字段）、回滚、提前清理保留版本 |

## 前置条件

- Go 1.25+
- Kubernetes 1.31+
- Ingress Controller（Ingress 模式，支持标准 K8s Ingress，如 Higress、nginx-ingress 等）
- Gateway API CRD 和 Gateway controller（Gateway 模式）；`Gateway/GatewayClass` 由平台预先创建
- kubectl 配置了目标集群

## 快速开始

### 1. 安装 CRD

```bash
kubectl apply -f config/crd/bases/gs.zzrr.io_gameservices.yaml
```

### 2. 运行 Operator

```bash
# 本地运行（开发调试）
make run

# 或编译后运行
go build -o bin/manager cmd/main.go
./bin/manager
```

### 3. 创建测试环境

```bash
# config/samples/zzrr_v1alpha1_gameservice.yaml 使用 adventure 命名空间、
# app=connector 标签和 3010 端口
kubectl create ns adventure

for i in 0 1 2; do
  kubectl run "connector-$i" -n adventure --image=nginx:alpine \
    --labels="app=connector,statefulset.kubernetes.io/pod-name=connector-$i" \
    --port=3010 -- /bin/sh -c "
echo 'connector-$i from adventure' > /usr/share/nginx/html/index.html
cat > /etc/nginx/conf.d/default.conf << 'EOF'
server {
    listen 3010 default_server;
    location / { root /usr/share/nginx/html; try_files \$uri /index.html =404; }
}
EOF
nginx -g 'daemon off;'"
done

kubectl wait --for=condition=Ready pod -n adventure -l app=connector --timeout=60s
```

### 4. 创建 GameService（Ingress 模式）

```yaml
# config/samples/zzrr_v1alpha1_gameservice.yaml
apiVersion: gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  labels:
    app.kubernetes.io/name: gs-operator
    app.kubernetes.io/managed-by: kustomize
  name: blue
spec:
  route:
    host: game.example.com
    pathType: Prefix
    pathPrefix: "/connector"
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
      higress.ingress.kubernetes.io/proxy-send-timeout: "300s"
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
  connectorNamespace: adventure
  podLabelKey: app
  podLabelValue: connector
  deployGroup:
    role: blue
    active: true
  retention:
    enabled: true
    defaultDuration: 24h
```

```bash
kubectl apply -f config/samples/zzrr_v1alpha1_gameservice.yaml
```

验证 GameService、Service 和 Ingress 自动创建：

```bash
kubectl get gameservice blue -o wide
kubectl get svc -n adventure -l app.kubernetes.io/managed-by=gs-operator
kubectl get ingress game-ingress-blue -n adventure
kubectl get ingress gamelogin-ingress-blue -n adventure
```

Gateway 模式下改用 `config/samples/zzrr_v1alpha1_gameservice_gateway.yaml`，Operator 会创建 `HTTPRoute`，不会创建 `Gateway`。Gateway 示例和 Ingress 示例使用相同的 `role: blue` 和 `connectorNamespace: adventure`，不要把两个活跃示例同时用于同一套测试入口：

```bash
kubectl delete gameservice blue --ignore-not-found
kubectl apply -f config/samples/zzrr_v1alpha1_gameservice_gateway.yaml
kubectl get gameservice blue-gateway -o wide
kubectl get httproute game-route-blue -n adventure
kubectl get httproute gamelogin-gateway-blue -n adventure
```

### 5. 测试访问

```bash
curl -s -H "Host: game.example.com" "http://<INGRESS_OR_GATEWAY_ADDRESS>/connector0"
# 返回: connector-0 from adventure
```

### 6. 蓝绿切换

```bash
# 假设已创建 name=green 的 Standby GameService，
# 并已同步设置 connectorNamespace、podLabelKey 和 podLabelValue

# 切换流量到 green
kubectl patch gameservice green --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice blue --type merge -p '{"spec":{"deployGroup":{"active":false}}}'

# Ingress 模式验证入口资源已切换
kubectl get ingress -A -o wide

# Gateway 模式改用
kubectl get httproute -A

# 测试访问（应返回 green 版本）
curl -s -H "Host: game.example.com" "http://<INGRESS_OR_GATEWAY_ADDRESS>/connector0"
# 返回: connector-0 from adventure-green
```

## CRD 参考

### GameService

```yaml
apiVersion: gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  name: <blue|green>
spec:
  trafficMode: string              # "Ingress" 或 "Gateway"，默认"Ingress"

  # --- Ingress 和 Gateway API 共用的路由配置 ---
  route:
    host: string                    # 入口 host，必填
    pathType: string                # "Prefix"、"Exact" 或 "ImplementationSpecific"，必填
    pathPrefix: string              # path 前缀，必填，如"/connector"
    port: int32                     # 后端 Service 端口

  # --- Ingress 配置；trafficMode=Ingress 时必填 ---
  ingress:
    ingressClassName: string        # Ingress Class，如"higress"
    tls:
      secretName: string            # TLS 证书 Secret（可选）
    annotations:                    # Ingress 注解（可选）
      key: value                    # 如 higress.ingress.kubernetes.io/proxy-read-timeout: "300s"

  # --- Gateway API 配置；trafficMode=Gateway 时必填 ---
  gateway:
    parentRef:
      name: string                  # Gateway 名称
      namespace: string             # Gateway namespace（可选）
      sectionName: string           # Gateway listener 名称（可选）

  # --- 额外入口配置；按 trafficMode 生成额外 Ingress 或 HTTPRoute（可选） ---
  extraIngress:
    name: string                    # 额外入口名称前缀
    annotations:                    # 额外 Ingress/HTTPRoute 注解（可选）
      key: value
    paths:
    - pathType: string              # "Prefix"、"Exact" 或 "ImplementationSpecific"
      path: string                  # 入口 path
      serviceName: string           # 后端 Service 名称
      port: int32                   # 后端 Service 端口

  # --- Connector 所在命名空间 ---
  connectorNamespace: string

  # --- Connector Pod 标签选择器 ---
  podLabelKey: string
  podLabelValue: string

  # --- 蓝绿发布 ---
  deployGroup:
    role: string                    # "blue" 或 "green"
    active: boolean                 # true=接收流量, false=不接收

  # --- 保留策略 ---
  retention:
    enabled: boolean
    defaultDuration: string         # 保留时长，如"24h"

status:
  conditions:
  - type: Available
    status: "True" | "False"
    reason: string
    message: string
  - type: TrafficActive
    status: "True" | "False"
    reason: string
  connectorCount: int32
  connectorImage: string
  observedGeneration: int64
```

### 关键字段说明

| 字段 | 必填 | 说明 |
|------|------|------|
| `trafficMode` | ❌ | 默认 `Ingress`；设置为 `Gateway` 时生成 HTTPRoute |
| `route.host` | ✅ | Ingress 和 HTTPRoute 共用的入口域名 |
| `route.pathType` | ✅ | 路径匹配类型 |
| `route.pathPrefix` | ✅ | 最终 path 为 `<pathPrefix><ordinal>` |
| `route.port` | ✅ | 后端 Service 端口 |
| `ingress` | Ingress 模式 ✅ | Ingress 专属配置 |
| `ingress.ingressClassName` | Ingress 模式 ✅ | Ingress Controller 名称 |
| `ingress.annotations` | ❌ | 仅 Ingress 模式透传到 Ingress 资源 |
| `ingress.tls.secretName` | ❌ | 仅 Ingress 模式配置 TLS 证书 |
| `gateway.parentRef.name` | Gateway 模式 ✅ | 平台预先创建的 Gateway 名称 |
| `gateway.parentRef.namespace` | ❌ | Gateway namespace，不填表示 HTTPRoute 本 namespace |
| `gateway.parentRef.sectionName` | ❌ | Gateway listener 名称 |
| `extraIngress` | ❌ | 额外入口配置；Ingress 模式生成额外 Ingress，Gateway 模式生成额外 HTTPRoute |
| `extraIngress.name` | `extraIngress` 存在时 ✅ | 额外入口名称前缀，最终名称形如 `<name>-ingress-<role>` 或 `<name>-gateway-<role>` |
| `extraIngress.paths` | `extraIngress` 存在时 ✅ | 额外入口 path 到已有 Service 的映射 |
| `connectorNamespace` | ✅ | Connector pod 所在的命名空间 |
| `podLabelKey` | ✅ | 用于筛选 Connector Pod 的标签 key |
| `podLabelValue` | ✅ | 用于筛选 Connector Pod 的标签 value |
| `deployGroup.role` | ✅ | 标识环境角色 |
| `deployGroup.active` | ✅ | 是否接收流量 |
| `retention` | ❌ | 非活跃时的保留策略 |

> 升级提示：原 `ingress.host/pathType/pathPrefix/port` 已迁移到 `route`，API group 已更新为 `gs.zzrr.io`，已有 GameService 清单还需要补齐 `podLabelKey` 和 `podLabelValue`。

## 蓝绿发布工作流

### 标准流程

```
1. 初始状态: Blue(v1.0) 活跃, Green 不存在

2. 部署新版本:
   ├── 创建 GameService/green (active: false)
   └── 在 adventure-green 命名空间部署 v2.0 游戏服

3. 验证 green 版本 → 触发切换:
   ├── kubectl patch gameservice green --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
   ├── kubectl patch gameservice blue --type merge -p '{"spec":{"deployGroup":{"active":false}}}'
   └── Operator 自动: 删除 blue 入口资源, 创建 green 入口资源

4. 保留期 (默认 24h):
   ├── blue 保留 24h 供回滚
   ├── 回滚: 把 active 切回 blue, green 设为 false
   └── 提前清理: kubectl delete gameservice blue
```

### 注意事项

- **切换必须手动**：patch active 字段触发，两个命令之间短暂存在双 active，Operator 保持当前入口资源不变
- **回滚**：反方向再执行一次 patch 即可
- **保留期到期**：Operator 自动删除非活跃的 GameService CR
- **只切换入口资源**：Operator 仅负责 Service、Ingress/HTTPRoute 和蓝绿入口切换，ArgoCD/用户负责部署和清理游戏服

## Pod 标签规约

Operator 通过 `spec.podLabelKey` 和 `spec.podLabelValue` 识别 Connector Pod。`config/samples/zzrr_v1alpha1_gameservice.yaml` 使用以下标签：

```yaml
labels:
  app: connector                                      # 与 podLabelKey/podLabelValue 对应
  statefulset.kubernetes.io/pod-name: connector-0     # 用于 Service selector
```

Service 仍使用 `statefulset.kubernetes.io/pod-name` 精确匹配单个 Pod：

```yaml
spec:
  selector:
    statefulset.kubernetes.io/pod-name: connector-0
```

## 开发指南

### 代码结构

```
api/v1alpha1/gameservice_types.go     # CRD 类型定义
internal/controller/
├── gameservice_controller.go         # 主 Reconcile 逻辑
├── connector_service.go              # Per-pod Service 管理
├── ingress_manager.go                # Ingress 创建/更新/删除
├── httproute_manager.go              # HTTPRoute 创建/更新/删除
└── extra_traffic.go                  # 额外入口命名和标签
config/
├── crd/bases/                        # CRD 定义（自动生成）
├── rbac/role.yaml                    # RBAC 权限（自动生成）
└── samples/                          # 示例 CR
cmd/main.go                           # 入口
```

### 常用命令

```bash
make generate         # 重新生成 DeepCopy 方法
make manifests        # 重新生成 CRD + RBAC
make build            # 编译
make run              # 本地运行
make test             # 运行单元测试
make lint-fix         # 代码格式化
```

### 修改类型后

修改 `api/v1alpha1/gameservice_types.go` 后必须执行：

```bash
make generate
make manifests
```

## 手动测试

完整测试指南见 `test/manual/README.md`：

```bash
# 启动 operator
nohup ./bin/manager > /tmp/operator.log 2>&1 &

# 创建测试环境
bash test/manual/setup-test-env.sh

# 按步骤测试
kubectl apply -f test/manual/gameservice-blue.yaml
# ...
```

## 常见问题

| 问题 | 原因 | 解决 |
|------|------|------|
| Ingress 未创建 | Operator 未运行或跨 namespace 错误 | 检查 `tail -f /tmp/operator.log` |
| 两个相同 Host 的 Ingress 冲突 | 切换时未删除旧 Ingress | 确认旧 CR 的 `active: false` |
| Service 未创建 | Pod 标签不匹配 | 检查 Pod 标签是否匹配 `spec.podLabelKey/spec.podLabelValue` |
| 访问 404 | Connector 应用没处理 Ingress Path | 配置 rewrite-target 或应用处理所有 path |
| Higress 返回 cluster_not_found | 跨 namespace 路由问题 | 确保 Ingress 和 Service 在同一 namespace |
| Operator 启动报端口占用 | 上次未正常退出 | `fuser -k 8081/tcp` |

## 核心原理

### Pod → Service 映射

```
Pod: connector-0  →  Service: connector-0-svc  (selector: statefulset.kubernetes.io/pod-name=connector-0)
Pod: connector-1  →  Service: connector-1-svc  (selector: statefulset.kubernetes.io/pod-name=connector-1)
```

### 入口 Path 生成

```
format: <pathPrefix><ordinal>
示例:  /connector0, /connector1, /connector2
```

### 蓝绿切换原理

Operator 在每个 Connector 命名空间中维护入口资源。切换时：
1. 新活跃 CR 在自己的 Connector 命名空间创建 Ingress 或 HTTPRoute
2. 旧非活跃 CR 删除自己 Connector 命名空间中的 Ingress 或 HTTPRoute
3. Ingress Controller 或 Gateway controller 根据最新配置路由

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0.
