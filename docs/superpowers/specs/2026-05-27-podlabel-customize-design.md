# Pod Label 自定义化设计

> 日期：2026-05-27 | 状态：已确认

## 背景

当前 gs-operator 通过硬编码的 pod 标签 `adventure=connector` 识别目标 Pod。用户需要将此标签改为可在 CR 中自定义，以适配不同的 Pod 标签命名约定。

## 涉及代码

| 文件 | 行号 | 当前行为 |
|------|------|---------|
| `connector_service.go` | 29 | `labels.NewRequirement("adventure", Equals, "connector")` |
| `gameservice_controller.go` | 307 | `pod.Labels["adventure"] != "connector"` |

## 设计决策

| 决策 | 选择 | 理由 |
|------|------|------|
| 配置层级 | CRD spec 字段 | 每个 GameService 独立指定标签，灵活性最强 |
| 自定义范围 | key + value 两者 | 适配任意标签命名约定 |
| 字段命名 | `podLabelKey` / `podLabelValue` | 语义清晰，避免与 connector 强绑定 |
| 是否必填 | 必填 | 用户明确要求，无需向后兼容 |
| 方案选择 | 两个简单字段 | 改动最小，不引入 K8s LabelSelector 的复杂度 |

## CRD API 变更

`api/v1alpha1/gameservice_types.go`，`GameServiceSpec` 新增：

```go
// +kubebuilder:validation:Required
// +kubebuilder:validation:MinLength=1
PodLabelKey string `json:"podLabelKey"`

// +kubebuilder:validation:Required
// +kubebuilder:validation:MinLength=1
PodLabelValue string `json:"podLabelValue"`
```

示例 CR：
```yaml
spec:
  connectorNamespace: adventure
  podLabelKey: app
  podLabelValue: game-server
  deployGroup:
    role: blue
    active: true
```

## Controller 变更

### ListConnectorPods

签名增加 `podLabelKey, podLabelValue string` 参数，用 `client.MatchingLabels` 替代硬编码的 `labels.NewRequirement`：

```go
func (m *ConnectorServiceManager) ListConnectorPods(
    ctx context.Context,
    namespace string,
    podLabelKey, podLabelValue string,
) ([]corev1.Pod, error) {
    var pods corev1.PodList
    if err := m.List(ctx, &pods,
        client.InNamespace(namespace),
        client.MatchingLabels{podLabelKey: podLabelValue},
    ); err != nil {
        return nil, fmt.Errorf("failed to list connector pods: %w", err)
    }
    return pods.Items, nil
}
```

### Reconcile 调用点

```go
pods, err := svcMgr.ListConnectorPods(ctx,
    gs.Spec.ConnectorNamespace,
    gs.Spec.PodLabelKey,
    gs.Spec.PodLabelValue,
)
```

### mapConnectorPodToGameService

去掉 `pod.Labels["adventure"] != "connector"` 早期过滤，改为 List 所有匹配 connectorNamespace 的 GS，逐 GS 比对 label：

```go
func (r *GameServiceReconciler) mapConnectorPodToGameService(ctx context.Context, obj client.Object) []reconcile.Request {
    pod, ok := obj.(*corev1.Pod)
    if !ok {
        return nil
    }

    log := log.FromContext(ctx)

    var list zzrrv1alpha1.GameServiceList
    if err := r.List(ctx, &list, client.MatchingFields{"spec.connectorNamespace": pod.Namespace}); err != nil {
        log.Error(err, "Failed to map pod to GameService", "pod", pod.Name)
        return nil
    }

    var requests []reconcile.Request
    for _, gs := range list.Items {
        if pod.Labels[gs.Spec.PodLabelKey] == gs.Spec.PodLabelValue {
            requests = append(requests, reconcile.Request{
                NamespacedName: types.NamespacedName{
                    Name:      gs.Name,
                    Namespace: gs.Namespace,
                },
            })
        }
    }
    return requests
}
```

**性能分析**：移除了硬编码标签的早期过滤，现在每个 pod 事件都会通过 controller-runtime informer cache（内存中的字段索引）list GameServices，再对每个 GS 做一次 O(1) map lookup 比对。list 走内存索引而非 API Server，per-GS 比对极轻量，影响可忽略。

## 同步更新的文件

| 文件 | 变更 |
|------|------|
| `api/v1alpha1/gameservice_types.go` | 新增 `PodLabelKey` / `PodLabelValue` 字段 |
| `internal/controller/connector_service.go` | `ListConnectorPods` 签名 + 实现 |
| `internal/controller/gameservice_controller.go` | Reconcile 调用点 + `mapConnectorPodToGameService` |
| `config/samples/zzrr_v1alpha1_gameservice.yaml` | 新增字段示例值 |
| `test/manual/gsb.yaml` | 新增字段 |
| `test/manual/gsg.yaml` | 新增字段 |
| `internal/controller/gameservice_controller_test.go` | 测试 Spec 必填字段 |
| `docs/implementation-deep-dive.md` | 更新 Spec 字段表 |

## 测试计划

1. **CRD 验证**：新建 CR 缺少 `podLabelKey`/`podLabelValue` → OpenAPI schema 拒绝
2. **ListConnectorPods**：自定义 label → 正确过滤匹配的 pod
3. **mapConnectorPodToGameService**：pod label 匹配 → 触发 Reconcile；不匹配 → 不触发
4. **单元测试**：完整流程测试（创建 GS → 创建匹配/不匹配 label 的 pod → 验证行为）

## 验证步骤

```
make manifests generate   # 重新生成 CRD 和 DeepCopy
make test                 # 运行单元测试
```
