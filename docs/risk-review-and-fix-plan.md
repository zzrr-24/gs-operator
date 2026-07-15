# gs-operator 风险审查与修复方案

> 审察日期：2026-05-26 | 审察范围：全项目代码、配置、CI/CD

---

## 目录

1. [风险 1：Pod→CR 映射错误被静默吞掉](#风险-1podcr-映射错误被静默吞掉)
2. [风险 2：并发 Service 创建失败后仍继续其余逻辑](#风险-2并发-service-创建失败后仍继续其余逻辑)
3. [风险 3：Retention 计时器 fallback 可能立即删除 CR](#风险-3retention-计时器-fallback-可能立即删除-cr)
4. [风险 4：Status Update 使用过期对象存在竞争](#风险-4status-update-使用过期对象存在竞争)

---

## 风险 1：Pod→CR 映射错误被静默吞掉

**严重等级**：🟠 High

**影响范围**：`internal/controller/gameservice_controller.go:297-299`

**问题描述**：

`mapConnectorPodToGameService` 中 `r.List` 失败时返回 `nil`，无任何日志。如果字段索引损坏或 API Server 暂时不可用，Pod 变更事件会被静默丢弃，导致 Ingress 路径与 Pod 实际状态不同步。

```go
// 当前代码（错误被吞掉）
func (r *GameServiceReconciler) mapConnectorPodToGameService(ctx context.Context, obj client.Object) []reconcile.Request {
    // ...
    var list zzrrv1alpha1.GameServiceList
    if err := r.List(ctx, &list, client.MatchingFields{"spec.connectorNamespace": pod.Namespace}); err != nil {
        return nil  // ← 静默失败，无日志，Pod 变更事件丢失
    }
    // ...
}
```

**修复方案**：

添加错误日志，暴露问题便于排查：

```go
func (r *GameServiceReconciler) mapConnectorPodToGameService(ctx context.Context, obj client.Object) []reconcile.Request {
    pod, ok := obj.(*corev1.Pod)
    if !ok {
        return nil
    }
    if pod.Labels["adventure"] != "connector" {
        return nil
    }

    log := log.FromContext(ctx)

    var list zzrrv1alpha1.GameServiceList
    if err := r.List(ctx, &list, client.MatchingFields{"spec.connectorNamespace": pod.Namespace}); err != nil {
        log.Error(err, "Failed to map connector pod to GameService",
            "pod", pod.Name,
            "namespace", pod.Namespace)
        return nil
    }

    var requests []reconcile.Request
    for _, gs := range list.Items {
        requests = append(requests, reconcile.Request{
            NamespacedName: types.NamespacedName{
                Name:      gs.Name,
                Namespace: gs.Namespace,
            },
        })
    }
    return requests
}
```

**工作量**：10min

---

## 风险 2：并发 Service 创建失败后仍继续其余逻辑

**严重等级**：🟠 High

**影响范围**：`internal/controller/gameservice_controller.go:92-116`

**问题描述**：

`eg.Wait()` 使用 `errgroup`，当任一 goroutine 返回 error 时，`eg.Wait()` 返回该 error。但当前代码在 `eg.Wait()` 返回 error 后仅做了日志记录，继续执行 `DeleteOrphanServices` 和 `ReconcileIngress`。

后果：
- 部分 Service 创建失败，但这些 Pod 的 ordinal 已被计入 `activeOrdinals`
- `DeleteOrphanServices` 以 `activeOrdinals` 为准，不会清理这些 Service
- `ReconcileIngress` 会生成指向不存在 Service 的 Ingress 路径

```go
// 当前代码
if err := eg.Wait(); err != nil {
    log.Error(err, "Failed to ensure services")
    // ← 继续执行，未 return
}

if err := svcMgr.DeleteOrphanServices(ctx, ...) {
    // ← 仍然执行
}

if gs.Spec.DeployGroup.Active {
    if err := ingMgr.ReconcileIngress(ctx, ...) {
        // ← 仍然执行，可能指向不存在的 Service
    }
}
```

**修复方案**：

Service 创建失败时应中止后续操作，等待下次 Reconcile 重试：

```go
{
    const maxConcurrency = 5
    sem := semaphore.NewWeighted(maxConcurrency)
    eg, egCtx := errgroup.WithContext(ctx)

    for i := range pods {
        pod := pods[i]
        eg.Go(func() error {
            if err := sem.Acquire(egCtx, 1); err != nil {
                return err
            }
            defer sem.Release(1)
            if _, err := svcMgr.EnsureService(egCtx, &pod, gs.Spec.Ingress.Port); err != nil {
                log.Error(err, "Failed to ensure service for pod", "pod", pod.Name)
                r.Recorder.Event(&gs, corev1.EventTypeWarning, "ServiceCreateFailed", err.Error())
                return err
            }
            return nil
        })
    }

    if err := eg.Wait(); err != nil {
        log.Error(err, "Failed to ensure services, skipping remaining reconciliation")
        r.Recorder.Event(&gs, corev1.EventTypeWarning, "ServiceEnsureFailed",
            "Some services could not be ensured, will retry")
        // 返回 error 触发 controller-runtime 的退避重试
        return ctrl.Result{}, err
    }
}
```

**工作量**：30min

---

## 风险 3：Retention 计时器 fallback 可能立即删除 CR

**严重等级**：🟠 High

**影响范围**：`internal/controller/gameservice_controller.go:216-223`

**问题描述**：

`getInactiveSince()` 在找不到 `TrafficActive=False` 条件时回退到 `CreationTimestamp`。如果一个 CR 是几天前创建的，但从未被标记为 `TrafficActive=False`（例如：首次创建时 `active=false`，或 condition 被意外清空），则该 CR 会立即被视为"已过期"并被删除。

```go
// 当前代码（有风险）
func (r *GameServiceReconciler) getInactiveSince(gs *zzrrv1alpha1.GameService) time.Time {
    for _, c := range gs.Status.Conditions {
        if c.Type == "TrafficActive" && c.Status == metav1.ConditionFalse {
            return c.LastTransitionTime.Time
        }
    }
    return gs.CreationTimestamp.Time  // ← 如果 CR 几天前创建，立即过期
}
```

**修复方案**：

找不到条件时使用当前时间作为 fallback（倒计时从此刻开始），同时记录日志便于运维排查 condition 丢失的根因：

```go
func (r *GameServiceReconciler) getInactiveSince(log logr.Logger, gs *zzrrv1alpha1.GameService) time.Time {
    for _, c := range gs.Status.Conditions {
        if c.Type == "TrafficActive" && c.Status == metav1.ConditionFalse {
            return c.LastTransitionTime.Time
        }
    }
    // 找不到 TrafficActive=False 条件时，从当前时间开始计算倒计时
    // 避免因 CreationTimestamp 过早而导致立即删除
    log.Info("No TrafficActive=False condition found, starting retention timer from now",
        "name", gs.Name, "namespace", gs.Namespace)
    return time.Now()
}
```

**额外保障**：结合 `setCondition` 确保"非 active 的 CR 首次 Reconcile 时"一定会设置 `TrafficActive=False` 条件（当前代码已做到这一点，此修复是兜底保护）。

**工作量**：15min

---

## 风险 4：Status Update 使用过期对象存在竞争

**严重等级**：🟠 High

**影响范围**：`internal/controller/gameservice_controller.go:145-167`

**问题描述**：

在整个 Reconcile 过程中，`gs` 对象在 Step 1（第 48 行）通过 `r.Get` 获取后一直沿用。期间：
1. 修改了 `gs.Status.ConnectorImage`（第 145-159 行）
2. 修改了 `gs.Status.ConnectorCount`（第 161 行）
3. 修改了 `gs.Status.ObservedGeneration`（第 162 行）
4. 通过 `setCondition` 修改了 `gs.Status.Conditions`（第 127-143 行）

多个 Reconcile 并发或外部 API 修改可能导致 `r.Status().Update` 因 `ResourceVersion` 冲突失败。

```go
// 当前代码
gs.Status.ConnectorImage = ""
// ... 修改 gs ...
gs.Status.ConnectorCount = int32(len(ordinals))
gs.Status.ObservedGeneration = gs.Generation

if err := r.Status().Update(ctx, &gs); err != nil {
    // ← 此时 gs.ResourceVersion 可能已过期
    log.Error(err, "Failed to update status")
    return ctrl.Result{}, err
}
```

**修复方案**：

在 Status 更新前重新 fetch 最新版本，然后选择性同步已变更的 status 字段：

```go
// 提取 connectorImage 计算逻辑
var connectorImage string
for i := range pods {
    if pods[i].Status.Phase == corev1.PodRunning && len(pods[i].Spec.Containers) > 0 {
        connectorImage = pods[i].Spec.Containers[0].Image
        break
    }
}
if connectorImage == "" {
    for i := range pods {
        if pods[i].Status.Phase == corev1.PodPending && len(pods[i].Spec.Containers) > 0 {
            connectorImage = pods[i].Spec.Containers[0].Image
            break
        }
    }
}

// 在 Status Update 之前重新获取最新版本
var latestGS zzrrv1alpha1.GameService
if err := r.Get(ctx, req.NamespacedName, &latestGS); err != nil {
    log.Error(err, "Failed to re-fetch GameService before status update")
    return ctrl.Result{}, err
}

// 将已变更的 status 字段同步到最新版本
latestGS.Status.ConnectorImage = connectorImage
latestGS.Status.ConnectorCount = int32(len(ordinals))
latestGS.Status.ObservedGeneration = latestGS.Generation
latestGS.Status.Conditions = gs.Status.Conditions  // setCondition 已修改了 gs.Status.Conditions

if err := r.Status().Update(ctx, &latestGS); err != nil {
    log.Error(err, "Failed to update status")
    return ctrl.Result{}, err
}
```

**工作量**：30min

---

## 修复计划

| 步骤 | 风险点 | 预计工作量 | 顺序理由 |
|------|--------|-----------|---------|
| Step 1 | 风险 1：Pod→CR 映射错误静默吞掉 | 10min | 改动最小，先做 |
| Step 2 | 风险 2：并发创建失败继续执行 | 30min | 数据一致性，高优先级；**必须在 Step 4 之前完成**，因为风险 2 在 `setCondition` 之前插入 `return`，先做风险 4 会导致代码冲突 |
| Step 3 | 风险 3：Retention 计时器 fallback | 15min | 影响自动删除逻辑 |
| Step 4 | 风险 4：Status 更新竞争 | 30min | 涉及代码结构微调，稍复杂；依赖 Step 2 完成 |

**总工作量**：约 1.5h

**修复后的验证步骤**：

1. `make lint-fix` — 代码格式检查
2. `make test` — 运行单元测试确保已有行为不被破坏
3. `make manifests generate` — 如涉及 API 变更则重新生成
4. 手动测试（可选）：
   - 删除 Pod → 确认 Event 日志中有错误信息
   - 模拟 Service 创建失败 → 确认 Reconcile 中止并重试
   - 创建 `active=false` 的 CR 且无 condition → 确认不会立即被删除
   - 并发修改 CR → 确认 Status Update 不冲突
