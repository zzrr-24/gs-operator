# Fix Slow Orphan Service Cleanup During Scale-Down (OwnerReference)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 缩容时 Service 跟随 Pod 自动删除，消除每轮 reconcile 只删一个 Svc 的慢速问题，同时利用 K8s 原生 GC 机制避免乒乓效应。

**Architecture:** 在 `EnsureService` 创建/更新 Service 时，为 Service 添加指向 Pod 的 `OwnerReference`。当 StatefulSet 缩容删除 Pod 时，Kubernetes 垃圾回收器自动级联删除该 Service。`DeleteOrphanServices` 保留作为快速清理兜底。被标记删除（`DeletionTimestamp != nil`）的 Pod 仍会被 GC 覆盖，无需额外过滤。

**Tech Stack:** Go, controller-runtime, Kubernetes OwnerReference / Garbage Collection

---

## 风险分析

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| **GC 延迟** | Pod 删除后 Service 可能留存数秒 | `DeleteOrphanServices` 作为同步快速清理路径 |
| **Pod 重建（滚动更新）** | 新 Pod 有不同 UID，旧 OwnerRef 失效 | Update 分支检测 UID 变化并更新 OwnerReference |
| **BlockOwnerDeletion** | 若设为 true，Pod 会因 Service 未删而阻塞 | 设为 `false`（或不设置，默认 false） |
| **跨 namespace** | OwnerReference 要求同 namespace | 当前 Service 和 Pod 已同 namespace，无影响 |
| **finalize 冲突** | GC 和 finalize 同时删 Service | `Delete` 对 NotFound 错误做 `IgnoreNotFound` 处理，无冲突 |

---

## 乒乓效应分析

**根因回顾**：日志中同一秒内 connector-15-svc 反复创建-删除，因为 StatefulSet 缩容过程中 Pod 的创建/删除与 Operator reconcile 存在竞态。

**OwnerReference 如何消除乒乓效应**：
- 一旦 Service 持有 Pod 的 OwnerReference，Service 的生命周期与 Pod 严格绑定
- `DeleteOrphanServices` 在 Pod 存在时不会误删其 Service（ordinal 在 activeOrdinals 中）
- Pod 被删后 GC 异步清理 Service，不会因为下一轮 reconcile 误重建
- **关键**：Service 不再是"可能被误判为 orphan"的对象，OwnerReference 提供了权威的归属关系

---

### Task 1: 修改 `EnsureService` — 添加 OwnerReference

**Files:**
- Modify: `internal/controller/connector_service.go:1-103`

- [ ] **Step 1: 添加 imports**

在 `connector_service.go` 的 import 块中新增 `"k8s.io/apimachinery/pkg/types"`：

修改前:
```go
import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)
```

修改后:
```go
import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)
```

- [ ] **Step 2: `desiredSvc` 添加 OwnerReference**

在 `desiredSvc` 构造函数中添加 `OwnerReferences`：

修改前 (lines 53-73):
```go
	desiredSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: pod.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "gs-operator",
				"gs-connector-ordinal":         ordinal,
			},
		},
		Spec: corev1.ServiceSpec{
			...
		},
	}
```

修改后:
```go
	desiredSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: pod.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "gs-operator",
				"gs-connector-ordinal":         ordinal,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "v1",
					Kind:               "Pod",
					Name:               pod.Name,
					UID:                pod.UID,
					BlockOwnerDeletion: ptr.To(false),
				},
			},
		},
		Spec: corev1.ServiceSpec{
			...
		},
	}
```

需要新增 import: `"k8s.io/utils/ptr"`（或用 `&[]bool{false}[0]` 替代，推荐用 `ptr.To`）。

- [ ] **Step 3: 添加含 `ptr.To` 的 import**

在 import 块中新增:
```go
	"k8s.io/utils/ptr"
```

或使用内联 bool 变量替代 `ptr.To(false)`:
```go
	blockOwnerDeletion := false
	// ...
	OwnerReferences: []metav1.OwnerReference{
		{
			APIVersion:         "v1",
			Kind:               "Pod",
			Name:               pod.Name,
			UID:                pod.UID,
			BlockOwnerDeletion: &blockOwnerDeletion,
		},
	},
```

- [ ] **Step 4: Update 分支添加 OwnerReference 比较逻辑**

在 `EnsureService` 的 Update 分支中，添加对 OwnerReference 的比较，确保 Pod 重建后（新 UID）能更新：

修改前 (lines 84-100):
```go
	needsUpdate := len(existingSvc.Spec.Ports) == 0 || existingSvc.Spec.Ports[0].Port != port

	for k, v := range desiredSvc.Labels {
		if existingSvc.Labels[k] != v {
			needsUpdate = true
			break
		}
	}

	if needsUpdate {
		existingSvc.Spec = desiredSvc.Spec
		existingSvc.Labels = desiredSvc.Labels
		if err := m.Update(ctx, &existingSvc); err != nil {
			return nil, fmt.Errorf("failed to update service %s: %w", svcName, err)
		}
		log.Info("Updated Service for connector pod", "service", svcName)
	}
```

修改后:
```go
	needsUpdate := len(existingSvc.Spec.Ports) == 0 || existingSvc.Spec.Ports[0].Port != port

	for k, v := range desiredSvc.Labels {
		if existingSvc.Labels[k] != v {
			needsUpdate = true
			break
		}
	}

	if !ownerRefContains(existingSvc.OwnerReferences, pod.UID) {
		needsUpdate = true
	}

	if needsUpdate {
		existingSvc.Spec = desiredSvc.Spec
		existingSvc.Labels = desiredSvc.Labels
		existingSvc.OwnerReferences = mergeOwnerRefs(existingSvc.OwnerReferences, desiredSvc.OwnerReferences, pod.UID)
		if err := m.Update(ctx, &existingSvc); err != nil {
			return nil, fmt.Errorf("failed to update service %s: %w", svcName, err)
		}
		log.Info("Updated Service for connector pod", "service", svcName)
	}
```

- [ ] **Step 5: 添加辅助函数**

在 `connector_service.go` 文件末尾添加两个辅助函数：

```go
func ownerRefContains(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range refs {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

func mergeOwnerRefs(existing, incoming []metav1.OwnerReference, podUID types.UID) []metav1.OwnerReference {
	merged := make([]metav1.OwnerReference, 0, len(existing)+1)
	seen := map[types.UID]bool{podUID: true}
	merged = append(merged, incoming...)
	for _, ref := range existing {
		if !seen[ref.UID] {
			seen[ref.UID] = true
			merged = append(merged, ref)
		}
	}
	return merged
}
```

---

### Task 2: 验证与测试

**Files:**
- 无新增文件

- [ ] **Step 6: 运行单元测试**

```bash
make test
```

Expected: 所有现有测试 PASS

- [ ] **Step 7: 运行 lint 检查**

```bash
make lint-fix
```

- [ ] **Step 8: Commit**

```bash
git add internal/controller/connector_service.go
git commit -m "fix: add Pod OwnerReference to Service for automatic GC cleanup on scale-down"
```

---

## 手动验证方式

按照 `test/manual/README.md` 进行手动验证：

```bash
# 1. 扩容
kubectl scale statefulset -n gsg connector --replicas 12

# 2. 确认 Service 存在且带有 OwnerReference
kubectl get svc connector-10-svc -n gsg -o jsonpath='{.metadata.ownerReferences}'
# 应输出: [{"apiVersion":"v1","blockOwnerDeletion":false,"kind":"Pod","name":"connector-10","uid":"..."}]

# 3. 缩容
kubectl scale statefulset -n gsg connector --replicas 5

# 4. 观察：Pod 10-11 被删除后，对应的 connector-10-svc/connector-11-svc 应被 GC 自动删除
#    再次 reconcile 时，DeleteOrphanServices 也会同步清理任何残留
kubectl get svc -n gsg -l app.kubernetes.io/managed-by=gs-operator

# 5. 观察日志：应在 Pod 被删除后很快看到 Service 消失
kubectl logs -n gs-operator-system deployment/gs-operator-controller-manager -c manager -f
```
