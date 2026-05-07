# gs-operator 代码优化实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复严重隐患 2 项、重要隐患 6 项、优化建议 6 项，共 14 项。核心变更：Finalizer 清理跨 namespace 资源 + annotations 合并策略 + field index 性能优化 + CRD 校验加严。

**Architecture:** 10 个独立 Task，TDD 顺序执行，每个 Task 可单独提交。Task 1 先做（影响 CRD 生成），Task 8 压轴（最大变更）。

**Tech Stack:** Go 1.25.3, controller-runtime v0.23.3, kubebuilder v4

---

## 一、文件影响范围总览

| 文件 | Task |
|------|------|
| `api/v1alpha1/gameservice_types.go` | Task 1 |
| `internal/controller/gameservice_controller.go` | Task 2, 3, 6, 7, 8, 10 |
| `internal/controller/connector_service.go` | Task 5 |
| `internal/controller/ingress_manager.go` | Task 4 |
| `internal/controller/gameservice_controller_test.go` | Task 10 |
| `config/manager/manager.yaml` | Task 9 |
| `config/crd/bases/zzrr.gs.zzrr.io_gameservices.yaml` | Task 1 (make manifests 自动生成) |
| `config/rbac/role.yaml` | Task 1 (make manifests 自动生成) |
| `go.mod` / `go.sum` | Task 6 (`golang.org/x/sync` indirect → direct) |

---

## 二、原始审查项与 Task 映射

| # | 审查项 | 严重程度 | 对应 Task |
|---|--------|----------|-----------|
| 1 | Rentention 到期删 CR，跨 namespace 子资源泄漏 | 🔴 严重 | Task 8 |
| 2 | `setCondition` 无条件更新 LastTransitionTime | 🔴 严重 | 本章末尾"额外修复" |
| 3 | `time.ParseDuration` 静默失败 | 🔴 严重 | Task 8 (合并到 retention 逻辑) |
| 4 | 跨 namespace 的 Ingress/Service 没有 Finalizer 清理 | 🟡 重要 | Task 8 |
| 5 | Ingress 全量覆盖 annotations/labels | 🟡 重要 | Task 4 |
| 6 | `mapConnectorPodToGameService` 全量 List | 🟡 重要 | Task 3 |
| 7 | Ingress 更新没有冲突重试 | 🟡 重要 | 本章末尾"额外修复" |
| 8 | `EnsureService` 不检测 spec 变更 | 🟡 重要 | Task 5 |
| 9 | `BuildConnectorOrdinals` 未被使用 | 🟢 优化 | Task 2 |
| 10 | Service Ensure 循环串行 | 🟢 优化 | Task 6 |
| 11 | 单元测试覆盖不足 | 🟢 优化 | Task 10 |
| 12 | Ingress 删除失败不发送 Event | 🟢 优化 | Task 7 |
| 13 | Deployment 内存/CPU 资源配置偏紧 | 🟢 优化 | Task 9 |
| 14 | CRD 缺少 validation markers | 🟢 优化 | Task 1 |

---

## 三、TDD 执行顺序

```
Task 1   → CRD 校验标记（最早做，影响 make manifests 输出）
Task 2   → 复用 BuildConnectorOrdinals（简单重构）
Task 3   → Field Index 性能优化
Task 4   → Annotations/Labels 合并策略
Task 5   → EnsureService spec 变更检测
Task 6   → Service Ensure 并行化
Task 7   → Ingress 删除失败 Event + condition
Task 8   → Finalizer + retention 改进（最大变更）
Task 9   → Deployment 资源配置
Task 10  → 单元测试覆盖（最后做，验证所有变更）
```

---

## Task 1: CRD 校验标记（加严版）

**覆盖审查项:** #14

**文件:**
- 修改: `api/v1alpha1/gameservice_types.go`
- 自动生成: `config/crd/bases/zzrr.gs.zzrr.io_gameservices.yaml`, `config/rbac/role.yaml`

### 1.1 完整源码

替换 `api/v1alpha1/gameservice_types.go` 全部内容：

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type IngressConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([a-zA-Z0-9\-\.]*[a-zA-Z0-9])?$`
	Host string `json:"host"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	IngressClassName string `json:"ingressClassName"`

	// +kubebuilder:validation:Enum=Prefix;Exact;ImplementationSpecific
	PathType string `json:"pathType"`

	// +kubebuilder:validation:MinLength=1
	PathPrefix string `json:"pathPrefix"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	TLS        *TLSConfig        `json:"tls,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type TLSConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
}

type DeployGroupConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=blue;green
	Role string `json:"role"`

	Active bool `json:"active"`
}

type RetentionConfig struct {
	Enabled bool `json:"enabled"`

	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*h$`
	DefaultDuration string `json:"defaultDuration"`
}

type GameServiceSpec struct {
	// +kubebuilder:validation:Required
	Ingress IngressConfig `json:"ingress"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ConnectorNamespace string `json:"connectorNamespace"`

	// +kubebuilder:validation:Required
	DeployGroup DeployGroupConfig `json:"deployGroup"`

	Retention *RetentionConfig `json:"retention,omitempty"`
}

type GameServiceStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ConnectorCount     int32              `json:"connectorCount,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type GameService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GameServiceSpec   `json:"spec"`
	Status            GameServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type GameServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GameService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GameService{}, &GameServiceList{})
}
```

### 1.2 校验规则汇总

| 字段 | 规则 | 说明 |
|------|------|------|
| `ingress.host` | Required, MinLength=1, Pattern hostname | 必填域名 |
| `ingress.ingressClassName` | Required, MinLength=1 | 必填 ingress class |
| `ingress.pathType` | Enum=Prefix;Exact;ImplementationSpecific | 限定标准 pathType |
| `ingress.pathPrefix` | MinLength=1 | 不可为空 |
| `ingress.port` | Required, 1-65535 | 必填合法端口 |
| `ingress.tls.secretName` | Required, MinLength=1 | 如配置 TLS 则必须指定 |
| `deployGroup.role` | Required, Enum=blue;green | 仅 blue/green |
| `retention.defaultDuration` | Pattern `^[1-9][0-9]*h$` | 仅小时单位，如 24h |
| `connectorNamespace` | Required, MinLength=1 | 必填命名空间 |

### 1.3 步骤

- [ ] 替换 `api/v1alpha1/gameservice_types.go` 内容
- [ ] 运行 `make manifests` 重新生成 CRD + RBAC
- [ ] 运行 `make generate` 重新生成 DeepCopy
- [ ] 运行 `make test` 确认无回归
- [ ] 运行 `make lint-fix`
- [ ] Commit: `feat: add strict CRD validation markers for all required fields`

---

## Task 2: 复用 BuildConnectorOrdinals

**覆盖审查项:** #9

**文件:** `internal/controller/gameservice_controller.go:59-68`

### 2.1 变更

**旧代码（第 59-68 行）：**
```go
	ordinals := make([]string, 0, len(pods))
	activeOrdinals := make(map[string]bool)
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}
		ord := GetPodOrdinal(pod.Name)
		ordinals = append(ordinals, ord)
		activeOrdinals[ord] = true
	}
```

**新代码：**
```go
	var podNames []string
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}
		podNames = append(podNames, pod.Name)
	}

	ordinals := ingMgr.BuildConnectorOrdinals(podNames)
	activeOrdinals := make(map[string]bool, len(ordinals))
	for _, ord := range ordinals {
		activeOrdinals[ord] = true
	}
```

- [ ] 编辑 `gameservice_controller.go`，替换 ordinals 构建逻辑
- [ ] 运行 `make test`
- [ ] Commit: `refactor: reuse BuildConnectorOrdinals in controller`

---

## Task 3: Field Index 加速 Pod→CR 映射

**覆盖审查项:** #6

**文件:** `internal/controller/gameservice_controller.go:152-189`

### 3.1 SetupWithManager 注册 index

替换 `SetupWithManager`:

```go
func (r *GameServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&zzrrv1alpha1.GameService{},
		"spec.connectorNamespace",
		func(rawObj client.Object) []string {
			gs := rawObj.(*zzrrv1alpha1.GameService)
			return []string{gs.Spec.ConnectorNamespace}
		},
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&zzrrv1alpha1.GameService{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapConnectorPodToGameService),
		).
		Named("gameservice").
		Complete(r)
}
```

### 3.2 修改 mapConnectorPodToGameService

替换为：

```go
func (r *GameServiceReconciler) mapConnectorPodToGameService(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	if pod.Labels["adventure"] != "connector" {
		return nil
	}

	var list zzrrv1alpha1.GameServiceList
	if err := r.List(ctx, &list, client.MatchingFields{"spec.connectorNamespace": pod.Namespace}); err != nil {
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

- [ ] 编辑 `gameservice_controller.go`，替换 SetupWithManager 和 mapConnectorPodToGameService
- [ ] 确认 `context`, `corev1`, `handler`, `types`, `reconcile` import 仍存在
- [ ] 运行 `make test`
- [ ] Commit: `perf: add field index for spec.connectorNamespace to speed up Pod→CR mapping`

---

## Task 4: Ingress Annotations/Labels 合并策略

**覆盖审查项:** #5

**文件:** `internal/controller/ingress_manager.go:123-125`

### 4.1 变更

**旧代码（第 123-125 行）：**
```go
	existingIngress.Spec = desiredIngress.Spec
	existingIngress.Annotations = desiredIngress.Annotations
	existingIngress.Labels = desiredIngress.Labels
```

**新代码：**
```go
	existingIngress.Spec = desiredIngress.Spec

	if existingIngress.Annotations == nil {
		existingIngress.Annotations = make(map[string]string)
	}
	for k, v := range desiredIngress.Annotations {
		existingIngress.Annotations[k] = v
	}

	if existingIngress.Labels == nil {
		existingIngress.Labels = make(map[string]string)
	}
	for k, v := range desiredIngress.Labels {
		existingIngress.Labels[k] = v
	}
```

### 4.2 合并行为说明

| 场景 | 行为 |
|------|------|
| CR 新增 annotation | 写入 Ingress |
| CR 修改已有 annotation 的值 | 覆盖 Ingress 上的值 |
| CR 删除了某个 annotation key | **不删除** Ingress 上的该 key（保守策略） |
| Ingress Controller 注入的 annotation（不在 CR 中） | **保留不变** |
| 现有 Ingress annotations 为 nil | 初始化空 map 后写入，正常处理 |

- [ ] 编辑 `ingress_manager.go`，替换 annotations/labels 更新逻辑
- [ ] 运行 `make test`
- [ ] Commit: `fix: merge Ingress annotations and labels instead of full replacement`

---

## Task 5: EnsureService 支持 spec 变更检测

**覆盖审查项:** #8

**文件:** `internal/controller/connector_service.go:48-86`

### 5.1 EnsureService 重写

替换整个 `EnsureService` 方法：

```go
func (m *ConnectorServiceManager) EnsureService(ctx context.Context, pod *corev1.Pod, port int32) (*corev1.Service, error) {
	log := log.FromContext(ctx)
	ordinal := GetPodOrdinal(pod.Name)
	svcName := fmt.Sprintf("connector-%s-svc", ordinal)

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
			Selector: map[string]string{
				"statefulset.kubernetes.io/pod-name": pod.Name,
			},
			Ports: []corev1.ServicePort{
				{
					Name: "connector",
					Port: port,
				},
			},
		},
	}

	var existingSvc corev1.Service
	if err := m.Get(ctx, client.ObjectKey{Name: svcName, Namespace: pod.Namespace}, &existingSvc); err != nil {
		if err := m.Create(ctx, desiredSvc); err != nil {
			return nil, fmt.Errorf("failed to create service %s: %w", svcName, err)
		}
		log.Info("Created Service for connector pod", "service", svcName, "pod", pod.Name)
		return desiredSvc, nil
	}

	needsUpdate := false
	if len(existingSvc.Spec.Ports) == 0 || existingSvc.Spec.Ports[0].Port != port {
		needsUpdate = true
	}
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

	return &existingSvc, nil
}
```

- [ ] 替换 `EnsureService` 方法
- [ ] 确认 `fmt`, `corev1`, `metav1`, `log`, `client.ObjectKey` import 仍存在
- [ ] 运行 `make test`
- [ ] Commit: `feat: detect and apply Service spec changes (port, labels)`

---

## Task 6: Service Ensure 并行化

**覆盖审查项:** #10

**文件:** `internal/controller/gameservice_controller.go:70-75`

### 6.1 Import 新增

在 `gameservice_controller.go` 的 import 块中添加：

```go
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
```

（`golang.org/x/sync v0.18.0` 已在 go.mod 中作为间接依赖存在，import 后自动提升为 direct。）

### 6.2 替换 Service ensure 循环

**旧代码（第 70-75 行）：**
```go
	for _, pod := range pods {
		if _, err := svcMgr.EnsureService(ctx, &pod, gs.Spec.Ingress.Port); err != nil {
			log.Error(err, "Failed to ensure service for pod", "pod", pod.Name)
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "ServiceCreateFailed", err.Error())
		}
	}
```

**新代码：**
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
			log.Error(err, "Failed to ensure services")
		}
	}
```

- [ ] 添加 `golang.org/x/sync/errgroup` 和 `golang.org/x/sync/semaphore` import
- [ ] 替换 Service ensure 循环
- [ ] 运行 `make test`
- [ ] Commit: `perf: parallelize Service Ensure with semaphore (max concurrency 5)`

---

## Task 7: Ingress 删除失败发送 Event + condition

**覆盖审查项:** #12

**文件:** `internal/controller/gameservice_controller.go:93-95`

### 7.1 变更

**旧代码（第 93-95 行）：**
```go
	} else {
		if err := ingMgr.DeleteIngress(ctx, &gs); err != nil {
			log.Error(err, "Failed to delete ingress for standby group")
		}
```

**新代码：**
```go
	} else {
		if err := ingMgr.DeleteIngress(ctx, &gs); err != nil {
			log.Error(err, "Failed to delete ingress for standby group")
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "IngressDeleteFailed", err.Error())
			r.setCondition(ctx, &gs, "Available", metav1.ConditionFalse, "IngressDeleteFailed", err.Error())
		}
```

- [ ] 编辑 `gameservice_controller.go`，替换 ingress 删除失败处理
- [ ] 运行 `make test`
- [ ] Commit: `fix: emit Event and set condition on Ingress delete failure`

---

## Task 8: Finalizer + retention 改进（最大变更）

**覆盖审查项:** #1, #3, #4

**文件:** `internal/controller/gameservice_controller.go`（多处）

### 8.1 新增 import

```go
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
```

### 8.2 新增常量

在 `GameServiceReconciler` 结构体之前：

```go
const gameServiceFinalizer = "zzrr.gs.zzrr.io/finalizer"
```

### 8.3 修改 Reconcile — 添加 deletionTimestamp 检测

在获取到 GS 之后（原第 47 行 `}` 之后），添加：

```go
	if !gs.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &gs)
	}

	if !controllerutil.ContainsFinalizer(&gs, gameServiceFinalizer) {
		controllerutil.AddFinalizer(&gs, gameServiceFinalizer)
		if err := r.Update(ctx, &gs); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
```

### 8.4 修改 Retention 逻辑 — 去掉手动删 Ingress + 添加 parse 失败告警

替换原第 110-128 行：

```go
	if !gs.Spec.DeployGroup.Active && gs.Spec.Retention != nil && gs.Spec.Retention.Enabled {
		duration, err := time.ParseDuration(gs.Spec.Retention.DefaultDuration)
		if err != nil {
			log.Error(err, "Invalid retention duration, using default 24h",
				"duration", gs.Spec.Retention.DefaultDuration)
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "InvalidRetentionDuration",
				fmt.Sprintf("Invalid duration %q, using default 24h", gs.Spec.Retention.DefaultDuration))
			duration = 24 * time.Hour
		}
		if gs.CreationTimestamp.Add(duration).Before(time.Now()) {
			log.Info("Retention period expired, deleting GameService", "name", gs.Name)
			if err := r.Delete(ctx, &gs); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		requeueAfter := time.Until(gs.CreationTimestamp.Add(duration))
		log.Info("Retention period active, will auto-delete", "requeueAfter", requeueAfter)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
```

与旧代码的区别：
1. `ParseDuration` 失败时发送 Event 告警，替换静默降级
2. 去掉 `ingMgr.DeleteIngress(ctx, &gs)` 的手动删除 —— Finalizer 负责清理
3. `r.Delete(ctx, &gs)` 后 K8s 设置 deletionTimestamp，下次 reconcile 触发 finalize

### 8.5 新增 finalize 方法

在 `setCondition` 方法之后（原第 151 行前）添加：

```go
func (r *GameServiceReconciler) finalize(ctx context.Context, gs *zzrrv1alpha1.GameService) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(gs, gameServiceFinalizer) {
		return ctrl.Result{}, nil
	}

	log := log.FromContext(ctx)
	log.Info("Finalizing GameService", "name", gs.Name)

	ingMgr := NewIngressManager(r.Client, r.Scheme)
	if err := ingMgr.DeleteIngress(ctx, gs); err != nil {
		log.Error(err, "Failed to delete ingress during finalization")
		return ctrl.Result{}, err
	}

	var svcList corev1.ServiceList
	if err := r.List(ctx, &svcList,
		client.InNamespace(gs.Spec.ConnectorNamespace),
		client.MatchingLabels{"app.kubernetes.io/managed-by": "gs-operator"},
	); err != nil {
		log.Error(err, "Failed to list services during finalization")
		return ctrl.Result{}, err
	}
	for i := range svcList.Items {
		svc := svcList.Items[i]
		if err := r.Delete(ctx, &svc); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete service during finalization", "service", svc.Name)
			return ctrl.Result{}, err
		}
		log.Info("Deleted Service during finalization", "service", svc.Name)
	}

	controllerutil.RemoveFinalizer(gs, gameServiceFinalizer)
	if err := r.Update(ctx, gs); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Finalization complete")
	return ctrl.Result{}, nil
}
```

### 8.6 完整 Reconcile 流程

```
1. Get GameService CR
2. if DeletionTimestamp != nil → finalize() → return
3. if Finalizer 不存在 → AddFinalizer + Update → return（触发下次 reconcile）
4. 原有逻辑：列 pod、构建 ordinal、EnsureService、DeleteOrphanServices
5. 原有逻辑：active 分支 ReconcileIngress / DeleteIngress
6. 原有逻辑：更新 Status
7. Retention 到期 → r.Delete(ctx, &gs) → K8s 设置 deletionTimestamp → 步骤 2 触发 finalize
```

### 8.7 RBAC 确认

Finalizer 需要的 RBAC 已存在（agent 注释中已有的 marker）：
```
// +kubebuilder:rbac:groups=zzrr.gs.zzrr.io,resources=gameservices/finalizers,verbs=update
```

Service 删除和 List 需要的 RBAC 也已存在：
```
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
```

无需新增 RBAC。

- [ ] 添加 `controllerutil` import
- [ ] 添加 `gameServiceFinalizer` 常量
- [ ] 添加 deletionTimestamp 检测和 finalizer 注册
- [ ] 添加 `finalize()` 方法
- [ ] 修改 retention 逻辑（Event 告警 + 去掉手动删 Ingress）
- [ ] 运行 `make test`
- [ ] 运行 `make lint-fix`
- [ ] Commit: `feat: add Finalizer to clean up cross-namespace resources on CR deletion`

---

## Task 9: Deployment 资源配置调整

**覆盖审查项:** #13

**文件:** `config/manager/manager.yaml:89-95`

### 9.1 变更

**旧代码：**
```yaml
        resources:
          limits:
            cpu: 500m
            memory: 128Mi
          requests:
            cpu: 10m
            memory: 64Mi
```

**新代码：**
```yaml
        resources:
          limits:
            cpu: 500m
            memory: 256Mi
          requests:
            cpu: 100m
            memory: 64Mi
```

- [ ] 编辑 `config/manager/manager.yaml`
- [ ] Commit: `chore: increase manager memory limit to 256Mi and CPU request to 100m`

---

## Task 10: 单元测试覆盖

**覆盖审查项:** #11

**文件:** `internal/controller/gameservice_controller_test.go`

### 10.1 测试文件

替换文件全部内容：

```go
package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

var _ = Describe("GameService Controller", func() {
	ctx := context.Background()

	var testNs string
	var typeNamespacedName types.NamespacedName

	BeforeEach(func() {
		testNs = "default"
		typeNamespacedName = types.NamespacedName{
			Name:      "test-gs",
			Namespace: testNs,
		}
	})

	AfterEach(func() {
		var gs zzrrv1alpha1.GameService
		err := k8sClient.Get(ctx, typeNamespacedName, &gs)
		if err != nil {
			return
		}
		controllerutil.RemoveFinalizer(&gs, gameServiceFinalizer)
		Expect(k8sClient.Update(ctx, &gs)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &gs)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, typeNamespacedName, &gs))
		}, time.Second*10, time.Millisecond*100).Should(BeTrue())
	})

	reconciler := func() *GameServiceReconciler {
		return &GameServiceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	}

	createGS := func(active bool) *zzrrv1alpha1.GameService {
		gs := &zzrrv1alpha1.GameService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      typeNamespacedName.Name,
				Namespace: typeNamespacedName.Namespace,
			},
			Spec: zzrrv1alpha1.GameServiceSpec{
				Ingress: zzrrv1alpha1.IngressConfig{
					Host:             "test.example.com",
					IngressClassName: "nginx",
					PathType:         "Prefix",
					PathPrefix:       "/connector",
					Port:             80,
				},
				ConnectorNamespace: testNs,
				DeployGroup: zzrrv1alpha1.DeployGroupConfig{
					Role:   "blue",
					Active: active,
				},
			},
		}
		Expect(k8sClient.Create(ctx, gs)).To(Succeed())
		return gs
	}

	reconcileOnce := func() {
		_, err := reconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: typeNamespacedName,
		})
		Expect(err).NotTo(HaveOccurred())
	}

	// ─── Test 1: Finalizer 自动注册 ───
	Context("When CR is first created", func() {
		It("should add finalizer on first reconcile", func() {
			gs := createGS(true)
			reconcileOnce()
			reconcileOnce()

			updated := &zzrrv1alpha1.GameService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gs.Name, Namespace: gs.Namespace}, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(gameServiceFinalizer))
		})
	})

	// ─── Test 2: 无 Pod 时的基础状态 ───
	Context("When no connector pods exist", func() {
		It("should set conditions and connectorCount=0", func() {
			gs := createGS(true)
			reconcileOnce()
			reconcileOnce()

			updated := &zzrrv1alpha1.GameService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gs.Name, Namespace: gs.Namespace}, updated)).To(Succeed())

			Expect(updated.Finalizers).To(ContainElement(gameServiceFinalizer))
			Expect(updated.Status.ConnectorCount).To(Equal(int32(0)))
			Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
		})
	})

	// ─── Test 3: Active → Inactive 切换 ───
	Context("When switching from active to inactive", func() {
		It("should set TrafficActive=False", func() {
			gs := createGS(true)
			reconcileOnce()
			reconcileOnce()

			var updated zzrrv1alpha1.GameService
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gs.Name, Namespace: gs.Namespace}, &updated)).To(Succeed())
			Expect(hasCondition(updated.Status.Conditions, "Available", metav1.ConditionTrue)).To(BeTrue())

			updated.Spec.DeployGroup.Active = false
			Expect(k8sClient.Update(ctx, &updated)).To(Succeed())
			reconcileOnce()

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gs.Name, Namespace: gs.Namespace}, &updated)).To(Succeed())
			Expect(hasCondition(updated.Status.Conditions, "TrafficActive", metav1.ConditionFalse)).To(BeTrue())
		})
	})

	// ─── Test 4: GetPodOrdinal 边界 ───
	Context("GetPodOrdinal", func() {
		It("should extract ordinal from pod name", func() {
			Expect(GetPodOrdinal("connector-0")).To(Equal("0"))
			Expect(GetPodOrdinal("connector-123")).To(Equal("123"))
			Expect(GetPodOrdinal("my-connector-abc-5")).To(Equal("5"))
			Expect(GetPodOrdinal("nohyphen")).To(Equal(""))
		})
	})

	// ─── Test 5: BuildConnectorOrdinals 去重排序 ───
	Context("BuildConnectorOrdinals", func() {
		It("should deduplicate and sort ordinals", func() {
			mgr := &IngressManager{}
			result := mgr.BuildConnectorOrdinals([]string{"pod-3", "pod-1", "pod-2", "pod-1"})
			Expect(result).To(Equal([]string{"1", "2", "3"}))
		})
	})
})

func hasCondition(conditions []metav1.Condition, condType string, status metav1.ConditionStatus) bool {
	for _, c := range conditions {
		if c.Type == condType && c.Status == status {
			return true
		}
	}
	return false
}
```

### 10.2 测试覆盖矩阵

| 测试用例 | 验证点 |
|----------|--------|
| CR first created | Finalizer 自动注册 |
| No pods exist | connectorCount=0, ObservedGeneration 同步 |
| Active→Inactive switch | TrafficActive 条件翻转 |
| GetPodOrdinal | 标准名称、多连字符、无连字符 |
| BuildConnectorOrdinals | 去重 + 排序 |

- [ ] 替换 `gameservice_controller_test.go`
- [ ] 运行 `make test`
- [ ] 运行 `make lint-fix`
- [ ] Commit: `test: add comprehensive unit tests for finalizer, switching, ordinals`

---

## 四、额外修复（不单独建 Task，随 Task 8 一并提交）

### A. setCondition 的 LastTransitionTime 优化

**文件:** `internal/controller/gameservice_controller.go:133-150`

**当前问题:** 每次调用 `setCondition` 都更新 `LastTransitionTime`，即使 condition 状态未变。

**修复:** 仅在 status/reason/message 有实质变化时才更新 `LastTransitionTime`：

```go
func (r *GameServiceReconciler) setCondition(ctx context.Context, gs *zzrrv1alpha1.GameService, condType string, status metav1.ConditionStatus, reason, message string) {
	for i, c := range gs.Status.Conditions {
		if c.Type == condType {
			if c.Status == status && c.Reason == reason && c.Message == message {
				return
			}
			gs.Status.Conditions[i].Status = status
			gs.Status.Conditions[i].Reason = reason
			gs.Status.Conditions[i].Message = message
			gs.Status.Conditions[i].LastTransitionTime = metav1.Now()
			return
		}
	}
	gs.Status.Conditions = append(gs.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: gs.Generation,
		LastTransitionTime: metav1.Now(),
	})
}
```

关键变化：添加了 `if c.Status == status && c.Reason == reason && c.Message == message { return }` 守卫。

### B. Ingress 更新使用 Patch 替代 Update

**文件:** `internal/controller/ingress_manager.go:127`

**当前问题:** `r.Update()` 在并发 reconcile 时可能因 ResourceVersion 冲突失败。

**修复:** 使用 `r.Patch()` 替代 `r.Update()`：

```go
	// 替换 m.Update(ctx, &existingIngress)
	if err := m.Patch(ctx, &existingIngress, client.MergeFrom(&existing)); err != nil {
		return fmt.Errorf("failed to patch ingress: %w", err)
	}
```

不过需要先保存原始状态用于 diff。改为：

```go
	var existingIngress networkingv1.Ingress
	if err := m.Get(ctx, client.ObjectKey{Name: ingressName, Namespace: gs.Spec.ConnectorNamespace}, &existingIngress); err != nil {
		// ... create logic ...
	}

	patch := client.MergeFrom(existingIngress.DeepCopy())
	existingIngress.Spec = desiredIngress.Spec

	if existingIngress.Annotations == nil {
		existingIngress.Annotations = make(map[string]string)
	}
	for k, v := range desiredIngress.Annotations {
		existingIngress.Annotations[k] = v
	}

	if existingIngress.Labels == nil {
		existingIngress.Labels = make(map[string]string)
	}
	for k, v := range desiredIngress.Labels {
		existingIngress.Labels[k] = v
	}

	if err := m.Patch(ctx, &existingIngress, patch); err != nil {
		return fmt.Errorf("failed to patch ingress: %w", err)
	}
```

---

## 五、最终验证清单

完成所有 Task 后执行：

```bash
make test        # 所有单元测试通过
make lint        # 无 lint 错误
make manifests   # CRD + RBAC 正确生成
make build       # 编译通过
```

手动验证（可选）：
- 创建 CR → 创建 connector pod → 验证 Service/Ingress 自动创建
- 删除 CR → 验证 Ingress 和 Service 被 Finalizer 清理
- CR 修改 annotations → 验证 Ingress annotations 合并后不丢失第三方 key
