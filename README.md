# gs-operator

游戏服 Kubernetes Operator — 自动管理 Connector Pod 的 Service 和 Ingress，支持蓝绿发布。

## 概述

gs-operator 是一个基于 Kubebuilder 的 Kubernetes Operator，用于管理游戏服 connector 的流量入口和版本更新。

### 解决的问题

- **去 Nginx Gateway**：通过 Ingress Path 直连 Connector Pod，减少网络跳转
- **动态 Ingress 管理**：Connector Pod 扩缩容时自动增删 Service 和 Ingress Path
- **蓝绿发布**：通过两个 GameService CR 管理两套游戏服环境，手动切换 Ingress 流量

### 架构

```
Higress (Ingress Controller)
    │
    ├── /connector0  →  Service: connector-0-svc  →  Pod: connector-0
    ├── /connector1  →  Service: connector-1-svc  →  Pod: connector-1
    ├── /connector2  →  Service: connector-2-svc  →  Pod: connector-2
    └── ...
```

### 职责边界

| 组件 | 职责 |
|------|------|
| **Operator** | 管理 Connector Pod 的 Service、Ingress Path、蓝绿切换 |
| **ArgoCD / 用户** | 部署游戏服本体（Deployment/StatefulSet/ConfigMap/Namespace）、清理旧版本 |
| **用户（手动）** | 触发蓝绿切换（patch `active` 字段）、回滚、提前清理保留版本 |

## 前置条件

- Go 1.25+
- Kubernetes 1.31+
- Ingress Controller（支持标准 K8s Ingress，如 Higress、nginx-ingress 等）
- kubectl 配置了目标集群

## 快速开始

### 1. 安装 CRD

```bash
kubectl apply -f config/crd/bases/zzrr.gs.zzrr.io_gameservices.yaml
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
# 创建两个命名空间代表蓝绿环境
kubectl create ns adventure-blue
kubectl create ns adventure-green

# 创建测试 Pod（每个命名空间 3 个）
for ns in adventure-blue adventure-green; do
  for i in 0 1 2; do
    kubectl run "connector-$i" -n "$ns" --image=nginx:alpine \
      --labels="adventure=connector,statefulset.kubernetes.io/pod-name=connector-$i" \
      --port=80 -- /bin/sh -c "
echo 'connector-$i from $ns' > /usr/share/nginx/html/index.html
cat > /etc/nginx/conf.d/default.conf << 'EOF2'
server {
    listen 80 default_server;
    location / { root /usr/share/nginx/html; try_files \$uri /index.html =404; }
}
EOF2
nginx -g 'daemon off;'"
  done
done
```

### 4. 创建 Blue GameService（活跃）

```yaml
# gameservice-blue.yaml
apiVersion: zzrr.gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  name: blue
  namespace: default
spec:
  ingress:
    host: game.example.com
    ingressClassName: higress
    pathType: Prefix
    pathPrefix: "/connector"
    port: 80
    annotations:
      higress.ingress.kubernetes.io/proxy-read-timeout: "300s"
      higress.ingress.kubernetes.io/proxy-send-timeout: "300s"
  connectorNamespace: adventure-blue
  deployGroup:
    role: blue
    active: true
  retention:
    enabled: true
    defaultDuration: 24h
```

```bash
kubectl apply -f gameservice-blue.yaml
```

验证 Service 和 Ingress 自动创建：

```bash
kubectl get svc -n adventure-blue -l app.kubernetes.io/managed-by=gs-operator
kubectl get ingress -n adventure-blue
```

### 5. 测试访问

```bash
NODE_IP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[0].address}')
curl -s -H "Host: game.example.com" "http://$NODE_IP:31546/connector0"
# 返回: connector-0 from adventure-blue
```

### 6. 蓝绿切换

```bash
# 创建 Green CR（Standby）
kubectl apply -f gameservice-green.yaml

# 切换流量到 green
kubectl patch gameservice green --type merge -p '{"spec":{"deployGroup":{"active":true}}}'
kubectl patch gameservice blue --type merge -p '{"spec":{"deployGroup":{"active":false}}}'

# 验证 Ingress 已切换
kubectl get ingress -A -o wide

# 测试访问（应返回 green 版本）
curl -s -H "Host: game.example.com" "http://$NODE_IP:31546/connector0"
# 返回: connector-0 from adventure-green
```

## CRD 参考

### GameService

```yaml
apiVersion: zzrr.gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  name: <blue|green>
  namespace: default
spec:
  # --- Ingress 配置 ---
  ingress:
    host: string                    # Ingress host，必填
    ingressClassName: string        # Ingress Class，如"higress"
    pathType: string                # 默认"Prefix"
    pathPrefix: string              # path 前缀，默认"/connector"
    port: int32                     # 后端 Service 端口
    tls:
      secretName: string            # TLS 证书 Secret（可选）
    annotations:                    # Ingress 注解（可选）
      key: value                    # 如 higress.ingress.kubernetes.io/proxy-read-timeout: "300s"

  # --- Connector 所在命名空间 ---
  connectorNamespace: string

  # --- 蓝绿发布 ---
  deployGroup:
    role: string                    # "blue" 或 "green"
    active: boolean                 # true=接收流量, false=不接收

  # --- 保留策略 ---
  retention:
    enabled: boolean
    defaultDuration: string         # 默认"24h"

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
  observedGeneration: int64
```

### 关键字段说明

| 字段 | 必填 | 说明 |
|------|------|------|
| `ingress.host` | ✅ | Ingress 域名 |
| `ingress.ingressClassName` | ✅ | Ingress Controller 名称 |
| `ingress.pathPrefix` | ❌ | 默认 `/connector`，最终 path 为 `<pathPrefix><ordinal>` |
| `ingress.port` | ❌ | 默认 3010 |
| `ingress.annotations` | ❌ | 透传到 Ingress 资源，k-v 格式 |
| `ingress.tls.secretName` | ❌ | 配置 TLS 证书 |
| `connectorNamespace` | ✅ | Connector pod 所在的命名空间 |
| `deployGroup.role` | ✅ | 标识环境角色 |
| `deployGroup.active` | ✅ | 是否接收流量 |
| `retention` | ❌ | 非活跃时的保留策略 |

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
   └── Operator 自动: 删除 blue Ingress, 创建 green Ingress

4. 保留期 (默认 24h):
   ├── blue 保留 24h 供回滚
   ├── 回滚: 把 active 切回 blue, green 设为 false
   └── 提前清理: kubectl delete gameservice blue
```

### 注意事项

- **切换必须手动**：patch active 字段触发，两个命令之间短暂存在双 active，Operator 保持当前 Ingress 不变
- **回滚**：反方向再执行一次 patch 即可
- **保留期到期**：Operator 自动删除非活跃的 GameService CR
- **只切换 Ingress**：Operator 仅负责流量入口切换，ArgoCD/用户负责部署和清理游戏服

## Pod 标签规约

Operator 通过以下标签识别 Connector Pod：

```yaml
labels:
  adventure: connector                                # 必须
  statefulset.kubernetes.io/pod-name: connector-0     # 用于 Service selector
```

Service 使用精确匹配：

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
└── ingress_manager.go                # Ingress 创建/更新/删除
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
| Service 未创建 | Pod 标签不匹配 | 检查 `adventure=connector` 标签 |
| 访问 404 | Connector 应用没处理 Ingress Path | 配置 rewrite-target 或应用处理所有 path |
| Higress 返回 cluster_not_found | 跨 namespace 路由问题 | 确保 Ingress 和 Service 在同一 namespace |
| Operator 启动报端口占用 | 上次未正常退出 | `fuser -k 8081/tcp` |

## 核心原理

### Pod → Service 映射

```
Pod: connector-0  →  Service: connector-0-svc  (selector: statefulset.kubernetes.io/pod-name=connector-0)
Pod: connector-1  →  Service: connector-1-svc  (selector: statefulset.kubernetes.io/pod-name=connector-1)
```

### Ingress Path 生成

```
format: <pathPrefix><ordinal>
示例:  /connector0, /connector1, /connector2
```

### 蓝绿切换原理

Operator 在每个 Connector 命名空间中维护一个 Ingress。切换时：
1. 新活跃 CR 创建 Ingress 到自己的命名空间
2. 旧非活跃 CR 删除自己命名空间的 Ingress
3. Ingress Controller 根据最新配置路由

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0.
