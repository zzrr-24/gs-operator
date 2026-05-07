# 添加 Version 列 + printcolumn 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** `kubectl get gameservices.gs.zzrr.io -A` 增加 Role、Active、Pods、Version 四列信息。

**Architecture:** 2 个源文件变更 + 自动生成 CRD，可选打印方式（默认 4 列，`-o wide` 再加 Version）。

**Tech Stack:** Go 1.25.3, controller-runtime v0.23.3, kubebuilder v4

---

## 边界情况矩阵

| 场景 | ConnectorImage 值 | printcolumn 显示 | 处理方式 |
|------|-------------------|-----------------|----------|
| 无 Pod（Pods=0） | `""` | 空列 | 默认空字符串 |
| Pod 有 containers，Running | `nginx:alpine` | 正常显示 | 取第一个 Running Pod |
| Pod 有 containers，Pending | `nginx:alpine` | 正常显示 | 回退到第一个 Pending Pod |
| Pod 无 containers（异常） | `""` | 空列 | `len(containers)==0` 时跳过 |
| 滚动更新中（多镜像混跑） | 第一个 Running Pod 的镜像 | 不精确 | 不做多数派判断，取第一个 |
| 镜像名很长（含 registry 前缀） | 完整值 | 可能被截断 | 不做截断，`-o wide` 可看完整 |
| Finalizer 首次 reconcile | `""` | 空列（短暂） | 该次 reconcile 不更新 status |
| Finalizer 清理（deletionTimestamp） | 不更新 | 不影响 | `finalize()` 不操作 status |
| 蓝绿切到 standby 后删 Pod | `""` | 空列 | 正确：无 Pod 则无版本 |
| 两个 reconcile 并行 | — | — | controller-runtime 自动重试 |

---

## 变更范围

| 文件 | 变更类型 | 内容 |
|------|----------|------|
| `api/v1alpha1/gameservice_types.go` | 修改 | 新增 `ConnectorImage` 字段 + 4 条 printcolumn marker |
| `internal/controller/gameservice_controller.go` | 修改 | 取 Pod 镜像写入 `gs.Status.ConnectorImage` |
| `api/v1alpha1/zz_generated.deepcopy.go` | 自动生成 | `make generate` |
| `config/crd/bases/gs.zzrr.io_gameservices.yaml` | 自动生成 | `make manifests` |

---

## Task 1: API 类型 + printcolumn

**文件:** `api/v1alpha1/gameservice_types.go`

### 1.1 GameServiceStatus 新增字段

```go
type GameServiceStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ConnectorCount     int32              `json:"connectorCount,omitempty"`
	ConnectorImage     string             `json:"connectorImage,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}
```

### 1.2 GameService 添加 4 条 printcolumn

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=".spec.deployGroup.role"
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=".spec.deployGroup.active"
// +kubebuilder:printcolumn:name="Pods",type=integer,JSONPath=".status.connectorCount"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".status.connectorImage"
type GameService struct {
```

目标显示效果：
```
NAMESPACE   NAME    ROLE    ACTIVE   PODS   VERSION        AGE
gsb         blue    blue    true     3      nginx:alpine   48m
gsg         green   green   false    0                     45m
```

- [ ] 编辑 `api/v1alpha1/gameservice_types.go`
- [ ] 运行 `make manifests && make generate`
- [ ] 运行 `make test` 确认无回归
- [ ] Commit: `feat: add ConnectorImage status field and printcolumn markers`

---

## Task 2: Controller 提取镜像信息

**文件:** `internal/controller/gameservice_controller.go`

### 2.1 插入位置

在第 144 行 `gs.Status.ConnectorCount = int32(len(ordinals))` 之前插入：

```go
	gs.Status.ConnectorImage = ""
	for i := range pods {
		if pods[i].Status.Phase == corev1.PodRunning && len(pods[i].Spec.Containers) > 0 {
			gs.Status.ConnectorImage = pods[i].Spec.Containers[0].Image
			break
		}
	}
	if gs.Status.ConnectorImage == "" {
		for i := range pods {
			if pods[i].Status.Phase == corev1.PodPending && len(pods[i].Spec.Containers) > 0 {
				gs.Status.ConnectorImage = pods[i].Spec.Containers[0].Image
				break
			}
		}
	}

	gs.Status.ConnectorCount = int32(len(ordinals))
```

### 2.2 取值策略

```
优先级: Running Pod > Pending Pod > ""
目的:   尽量反映真实运行版本
```

两步循环：
1. 先遍历 Running Pod，取第一个的容器镜像
2. 如果没有 Running Pod，遍历 Pending Pod，取第一个的
3. 如果都没有，保持空字符串

- [ ] 编辑 `internal/controller/gameservice_controller.go`
- [ ] 运行 `make test` 确认无回归
- [ ] 运行 `make lint-fix`
- [ ] Commit: `feat: populate ConnectorImage from running/pending connector pods`

---

## Task 3: 构建 + 推送 + 部署

- [ ] 停止当前 operator: `pkill -f "bin/manager"`
- [ ] 构建: `make build`
- [ ] 构建镜像: `podman build --network=host -t registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v1 .`
- [ ] 推送: `podman push registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v1`
- [ ] 重新生成部署 YAML: `make build-installer IMG=registry.cn-hangzhou.aliyuncs.com/zzrr_images/gs-operator:v1`
- [ ] 部署: `kubectl apply -f dist/install.yaml`
- [ ] 验证: `kubectl get gameservices.gs.zzrr.io -A`
- [ ] 滚动重启 Deployment: `kubectl rollout restart deployment gs-operator-controller-manager -n gs-operator-system`

---

## 验证清单

```bash
# 1. CRD 列展示
kubectl get gameservices.gs.zzrr.io -A

# 2. Expect:
# NAMESPACE   NAME    ROLE    ACTIVE   PODS   VERSION        AGE
# gsb         blue    blue    true     3      nginx:alpine   XXm
# gsg         green   green   false    0                    XXm

# 3. unit test
make test

# 4. lint
make lint
```
