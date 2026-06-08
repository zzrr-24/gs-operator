# 附加入口蓝绿切换 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 `GameService` 增加可选的 `spec.extraIngress`，让附加入口随蓝绿 active 状态和 `trafficMode` 同步管理 Ingress 或 HTTPRoute。

**Architecture:** API 层新增 `ExtraIngressConfig` 和 `ExtraIngressPath`，只描述资源基础名、annotations、paths 和直接可调用的后端 Service。控制器复用现有 `IngressManager` 和 `HTTPRouteManager`，通过统一 helper 生成 `{name}-{strings.ToLower(trafficMode)}-{role}` 资源名，并在 active/standby/finalizer 分支中与主入口一起 reconcile 或清理。

**Tech Stack:** Go 1.25.3, controller-runtime, Kubernetes networking.k8s.io/v1 Ingress, Gateway API HTTPRoute, Ginkgo/Gomega, Kubebuilder controller-tools.

---

## 文件结构

- Modify: `api/v1alpha1/gameservice_types.go`
  增加 `ExtraIngressConfig`、`ExtraIngressPath` 和 `GameServiceSpec.ExtraIngress`。
- Modify: `api/v1alpha1/zz_generated.deepcopy.go`
  由 `make generate` 自动生成，不能手改。
- Modify: `config/crd/bases/gs.zzrr.io_gameservices.yaml`
  由 `make manifests` 自动生成，不能手改。
- Create: `internal/controller/extra_traffic.go`
  放置附加入口资源名、labels、path type 转换等共享 helper。
- Create: `internal/controller/extra_traffic_test.go`
  覆盖资源名、labels、HTTPRoute path type 转换 helper。
- Modify: `internal/controller/ingress_manager.go`
  增加附加 Ingress reconcile/delete 方法。
- Modify: `internal/controller/httproute_manager.go`
  增加附加 HTTPRoute reconcile/delete 方法。
- Create: `internal/controller/ingress_manager_extra_test.go`
  覆盖附加 Ingress manager 的 create/update/delete 行为。
- Create: `internal/controller/httproute_manager_extra_test.go`
  覆盖附加 HTTPRoute manager 的 create/update/delete 行为。
- Modify: `internal/controller/gameservice_controller.go`
  在 active、standby、finalizer 分支接入附加入口管理。
- Modify: `internal/controller/gameservice_controller_test.go`
  增加 controller 级测试，验证 active/standby、Ingress/Gateway、陈旧资源清理。
- Modify: `config/samples/zzrr_v1alpha1_gameservice.yaml`
  增加 Ingress 模式附加入口样例。
- Modify: `config/samples/zzrr_v1alpha1_gameservice_gateway.yaml`
  增加 Gateway 模式附加入口样例。

## Task 1: API 类型与 CRD 字段

**Files:**
- Modify: `api/v1alpha1/gameservice_types.go:73-108`
- Generated: `api/v1alpha1/zz_generated.deepcopy.go`
- Generated: `config/crd/bases/gs.zzrr.io_gameservices.yaml`

- [ ] **Step 1: 写入会先失败的 controller 测试片段**

在 `internal/controller/gameservice_controller_test.go` 的 `createGS` helper 返回前临时加入 `ExtraIngress` 字段引用，让当前编译先失败，证明 API 字段尚不存在：

```go
ExtraIngress: &zzrrv1alpha1.ExtraIngressConfig{
	Name: "gamelogin",
	Paths: []zzrrv1alpha1.ExtraIngressPath{
		{
			PathType:    "Prefix",
			Path:        "/serverlogin",
			ServiceName: "logingame-blue",
			Port:        9020,
		},
	},
},
```

- [ ] **Step 2: 运行测试确认失败**

Run:

```bash
go test ./internal/controller -run TestControllers -ginkgo.focus "GameService Controller" -count=1
```

Expected: FAIL，错误包含：

```text
undefined: zzrrv1alpha1.ExtraIngressConfig
undefined: zzrrv1alpha1.ExtraIngressPath
unknown field ExtraIngress in struct literal of type v1alpha1.GameServiceSpec
```

- [ ] **Step 3: 增加 API 类型**

在 `api/v1alpha1/gameservice_types.go` 中，放在 `IngressConfig` 后面、`TrafficMode` 前面：

```go
type ExtraIngressConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	Annotations map[string]string `json:"annotations,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Paths []ExtraIngressPath `json:"paths"`
}

type ExtraIngressPath struct {
	// +kubebuilder:validation:Enum=Prefix;Exact;ImplementationSpecific
	PathType string `json:"pathType"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ServiceName string `json:"serviceName"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}
```

在 `GameServiceSpec` 的 `Ingress`/`Gateway` 字段后面加入：

```go
	ExtraIngress *ExtraIngressConfig `json:"extraIngress,omitempty"`
```

- [ ] **Step 4: 运行最小编译确认 API 通过**

Run:

```bash
go test ./internal/controller -run TestControllers -ginkgo.focus "GameService Controller" -count=1
```

Expected: 仍然可能因为临时测试逻辑没有行为实现而失败，但不再出现 `ExtraIngressConfig`、`ExtraIngressPath`、`ExtraIngress` 未定义错误。

- [ ] **Step 5: 移除临时编译探针**

从 `internal/controller/gameservice_controller_test.go` 的 `createGS` helper 中移除 Step 1 加入的临时 `ExtraIngress` 字段引用。Task 3 会写入正式的 controller 测试。

Run:

```bash
git diff -- internal/controller/gameservice_controller_test.go
```

Expected: 没有 Step 1 加入的临时 `ExtraIngress` 字段引用残留。

- [ ] **Step 6: 生成 DeepCopy 和 CRD**

Run:

```bash
make generate
make manifests
```

Expected: PASS，并更新：

```text
api/v1alpha1/zz_generated.deepcopy.go
config/crd/bases/gs.zzrr.io_gameservices.yaml
```

- [ ] **Step 7: 提交 API 变更**

Run:

```bash
git add api/v1alpha1/gameservice_types.go api/v1alpha1/zz_generated.deepcopy.go config/crd/bases/gs.zzrr.io_gameservices.yaml
git commit -m "feat: add extra ingress api fields"
```

Expected: commit 成功。

## Task 2: 附加入口公共 helper

**Files:**
- Create: `internal/controller/extra_traffic.go`
- Create: `internal/controller/extra_traffic_test.go`

- [ ] **Step 1: 写 failing unit tests**

创建 `internal/controller/extra_traffic_test.go`：

```go
package controller

import (
	"testing"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

func TestExtraTrafficName(t *testing.T) {
	gs := &zzrrv1alpha1.GameService{
		Spec: zzrrv1alpha1.GameServiceSpec{
			ExtraIngress: &zzrrv1alpha1.ExtraIngressConfig{Name: "gamelogin"},
			DeployGroup: zzrrv1alpha1.DeployGroupConfig{Role: "blue"},
		},
	}

	got := extraTrafficName(gs, zzrrv1alpha1.TrafficModeIngress)
	want := "gamelogin-ingress-blue"
	if got != want {
		t.Fatalf("extraTrafficName() = %q, want %q", got, want)
	}
}

func TestExtraTrafficLabels(t *testing.T) {
	labels := extraTrafficLabels("green")

	if labels["app.kubernetes.io/managed-by"] != "gs-operator" {
		t.Fatalf("managed-by label = %q", labels["app.kubernetes.io/managed-by"])
	}
	if labels["gs-role"] != "green" {
		t.Fatalf("gs-role label = %q", labels["gs-role"])
	}
	if labels["gs-extra-traffic"] != "true" {
		t.Fatalf("gs-extra-traffic label = %q", labels["gs-extra-traffic"])
	}
}

func TestExtraHTTPPathType(t *testing.T) {
	exact := extraHTTPPathType("Exact")
	if exact != gatewayv1.PathMatchExact {
		t.Fatalf("exact type = %q", exact)
	}

	prefix := extraHTTPPathType("Prefix")
	if prefix != gatewayv1.PathMatchPathPrefix {
		t.Fatalf("prefix type = %q", prefix)
	}

	implementationSpecific := extraHTTPPathType("ImplementationSpecific")
	if implementationSpecific != gatewayv1.PathMatchPathPrefix {
		t.Fatalf("implementation specific type = %q", implementationSpecific)
	}
}
```

- [ ] **Step 2: 运行测试确认 helper 未定义失败**

Run:

```bash
go test ./internal/controller -run 'TestExtraTraffic' -count=1
```

Expected: FAIL，错误包含：

```text
undefined: extraTrafficName
undefined: extraTrafficLabels
undefined: extraHTTPPathType
```

- [ ] **Step 3: 实现 helper**

创建 `internal/controller/extra_traffic.go`：

```go
package controller

import (
	"fmt"
	"strings"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

func extraTrafficName(gs *zzrrv1alpha1.GameService, mode zzrrv1alpha1.TrafficMode) string {
	return fmt.Sprintf("%s-%s-%s",
		gs.Spec.ExtraIngress.Name,
		strings.ToLower(string(mode)),
		gs.Spec.DeployGroup.Role,
	)
}

func extraTrafficLabels(role string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "gs-operator",
		"gs-role":                      role,
		"gs-extra-traffic":             "true",
	}
}

func extraHTTPPathType(pathType string) gatewayv1.PathMatchType {
	if pathType == "Exact" {
		return gatewayv1.PathMatchExact
	}
	return gatewayv1.PathMatchPathPrefix
}
```

- [ ] **Step 4: 运行 helper 测试确认通过**

Run:

```bash
go test ./internal/controller -run 'TestExtraTraffic|TestExtraHTTPPathType' -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交 helper**

Run:

```bash
git add internal/controller/extra_traffic.go internal/controller/extra_traffic_test.go
git commit -m "feat: add extra traffic helpers"
```

Expected: commit 成功。

## Task 3: 附加 Ingress 管理

**Files:**
- Modify: `internal/controller/ingress_manager.go`
- Create: `internal/controller/ingress_manager_extra_test.go`

- [ ] **Step 1: 写 Ingress manager failing tests**

创建 `internal/controller/ingress_manager_extra_test.go`：

```go
package controller

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

func TestReconcileExtraIngress(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := zzrrv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	mgr := NewIngressManager(k8sClient, scheme)
	gs := extraIngressTestGameService()

	if err := mgr.ReconcileExtraIngress(ctx, gs); err != nil {
		t.Fatalf("ReconcileExtraIngress() error = %v", err)
	}

	ing := &networkingv1.Ingress{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: "adventure"}, ing); err != nil {
		t.Fatalf("failed to get extra ingress: %v", err)
	}
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "higress" {
		t.Fatalf("ingress class = %v", ing.Spec.IngressClassName)
	}
	if got := ing.Spec.Rules[0].Host; got != "adventure.zzrr.io" {
		t.Fatalf("host = %q", got)
	}
	if got := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name; got != "logingame-blue" {
		t.Fatalf("service name = %q", got)
	}
	if got := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number; got != 9020 {
		t.Fatalf("service port = %d", got)
	}
	if got := ing.Annotations["extra-only"]; got != "true" {
		t.Fatalf("extra annotation = %q", got)
	}
	if _, exists := ing.Annotations["main-only"]; exists {
		t.Fatalf("main annotation should not be copied")
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "game-tls" {
		t.Fatalf("tls = %#v", ing.Spec.TLS)
	}
}

func TestDeleteExtraIngress(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := zzrrv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-ingress-blue", Namespace: "adventure"},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	mgr := NewIngressManager(k8sClient, scheme)

	if err := mgr.DeleteExtraIngress(ctx, extraIngressTestGameService()); err != nil {
		t.Fatalf("DeleteExtraIngress() error = %v", err)
	}

	err := k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: "adventure"}, &networkingv1.Ingress{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func extraIngressTestGameService() *zzrrv1alpha1.GameService {
	return &zzrrv1alpha1.GameService{
		ObjectMeta: metav1.ObjectMeta{Name: "blue", Namespace: "control"},
		Spec: zzrrv1alpha1.GameServiceSpec{
			Route: zzrrv1alpha1.RouteConfig{
				Host:       "adventure.zzrr.io",
				PathType:   "Prefix",
				PathPrefix: "/connector",
				Port:       3010,
			},
			Ingress: &zzrrv1alpha1.IngressConfig{
				IngressClassName: "higress",
				TLS: &zzrrv1alpha1.TLSConfig{
					SecretName: "game-tls",
				},
				Annotations: map[string]string{"main-only": "true"},
			},
			ConnectorNamespace: "adventure",
			DeployGroup:       zzrrv1alpha1.DeployGroupConfig{Role: "blue", Active: true},
			ExtraIngress: &zzrrv1alpha1.ExtraIngressConfig{
				Name:        "gamelogin",
				Annotations: map[string]string{"extra-only": "true"},
				Paths: []zzrrv1alpha1.ExtraIngressPath{
					{PathType: "Prefix", Path: "/serverlogin", ServiceName: "logingame-blue", Port: 9020},
				},
			},
		},
	}
}
```

- [ ] **Step 2: 运行 manager 测试确认失败**

Run:

```bash
go test ./internal/controller -run 'TestReconcileExtraIngress|TestDeleteExtraIngress' -count=1
```

Expected: FAIL，错误包含：

```text
undefined: (*IngressManager).ReconcileExtraIngress
undefined: (*IngressManager).DeleteExtraIngress
```

- [ ] **Step 3: 实现 `ReconcileExtraIngress` 和 `DeleteExtraIngress`**

在 `internal/controller/ingress_manager.go` 末尾增加：

```go
func (m *IngressManager) ReconcileExtraIngress(ctx context.Context, gs *zzrrv1alpha1.GameService) error {
	log := log.FromContext(ctx)
	if gs.Spec.ExtraIngress == nil {
		return nil
	}
	if gs.Spec.Ingress == nil {
		return fmt.Errorf("ingress config is required when reconciling extra ingress")
	}

	ingressName := extraTrafficName(gs, zzrrv1alpha1.TrafficModeIngress)
	paths := make([]networkingv1.HTTPIngressPath, 0, len(gs.Spec.ExtraIngress.Paths))
	for _, configuredPath := range gs.Spec.ExtraIngress.Paths {
		pathType := networkingv1.PathTypePrefix
		if configuredPath.PathType != "" {
			pathType = networkingv1.PathType(configuredPath.PathType)
		}
		paths = append(paths, networkingv1.HTTPIngressPath{
			Path:     configuredPath.Path,
			PathType: &pathType,
			Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: configuredPath.ServiceName,
					Port: networkingv1.ServiceBackendPort{
						Number: configuredPath.Port,
					},
				},
			},
		})
	}

	desiredIngress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        ingressName,
			Namespace:   gs.Spec.ConnectorNamespace,
			Labels:      extraTrafficLabels(gs.Spec.DeployGroup.Role),
			Annotations: maps.Clone(gs.Spec.ExtraIngress.Annotations),
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &gs.Spec.Ingress.IngressClassName,
			Rules: []networkingv1.IngressRule{
				{
					Host: gs.Spec.Route.Host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: paths,
						},
					},
				},
			},
		},
	}

	if gs.Spec.Ingress.TLS != nil {
		desiredIngress.Spec.TLS = []networkingv1.IngressTLS{
			{
				Hosts:      []string{gs.Spec.Route.Host},
				SecretName: gs.Spec.Ingress.TLS.SecretName,
			},
		}
	}

	if gs.Namespace == gs.Spec.ConnectorNamespace {
		if err := controllerutil.SetControllerReference(gs, desiredIngress, m.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
	}

	var existingIngress networkingv1.Ingress
	if err := m.Get(ctx, client.ObjectKey{Name: ingressName, Namespace: gs.Spec.ConnectorNamespace}, &existingIngress); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get extra ingress: %w", err)
		}
		if err := m.Create(ctx, desiredIngress); err != nil {
			return fmt.Errorf("failed to create extra ingress: %w", err)
		}
		log.Info("Created extra Ingress", "ingress", ingressName)
		return nil
	}

	existingIngress.Spec = desiredIngress.Spec
	existingIngress.Annotations = maps.Clone(desiredIngress.Annotations)
	existingIngress.Labels = maps.Clone(desiredIngress.Labels)
	if err := m.Update(ctx, &existingIngress); err != nil {
		return fmt.Errorf("failed to update extra ingress: %w", err)
	}

	log.Info("Updated extra Ingress", "ingress", ingressName, "paths", len(paths))
	return nil
}

func (m *IngressManager) DeleteExtraIngress(ctx context.Context, gs *zzrrv1alpha1.GameService) error {
	if gs.Spec.ExtraIngress == nil {
		return nil
	}
	ingressName := extraTrafficName(gs, zzrrv1alpha1.TrafficModeIngress)
	var ing networkingv1.Ingress
	if err := m.Get(ctx, client.ObjectKey{Name: ingressName, Namespace: gs.Spec.ConnectorNamespace}, &ing); err != nil {
		return client.IgnoreNotFound(err)
	}
	return m.Delete(ctx, &ing)
}
```

- [ ] **Step 4: 运行 Ingress manager 测试确认通过**

Run:

```bash
go test ./internal/controller -run 'TestReconcileExtraIngress|TestDeleteExtraIngress' -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交 Ingress manager 方法**

Run:

```bash
git add internal/controller/ingress_manager.go internal/controller/ingress_manager_extra_test.go
git commit -m "feat: add extra ingress manager"
```

Expected: commit 成功。

## Task 4: 附加 HTTPRoute 管理

**Files:**
- Modify: `internal/controller/httproute_manager.go`
- Create: `internal/controller/httproute_manager_extra_test.go`

- [ ] **Step 1: 写 HTTPRoute manager failing tests**

创建 `internal/controller/httproute_manager_extra_test.go`：

```go
package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

func TestReconcileExtraHTTPRoute(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := gatewayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := zzrrv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	mgr := NewHTTPRouteManager(k8sClient, scheme)
	gs := extraHTTPRouteTestGameService()

	if err := mgr.ReconcileExtraHTTPRoute(ctx, gs); err != nil {
		t.Fatalf("ReconcileExtraHTTPRoute() error = %v", err)
	}

	route := &gatewayv1.HTTPRoute{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: "adventure"}, route); err != nil {
		t.Fatalf("failed to get extra httproute: %v", err)
	}
	if got := route.Annotations["extra-route"]; got != "true" {
		t.Fatalf("extra annotation = %q", got)
	}
	if got := string(route.Spec.ParentRefs[0].Name); got != "shared-gateway" {
		t.Fatalf("parent name = %q", got)
	}
	if route.Spec.ParentRefs[0].Namespace == nil || string(*route.Spec.ParentRefs[0].Namespace) != "gateway-system" {
		t.Fatalf("parent namespace = %#v", route.Spec.ParentRefs[0].Namespace)
	}
	if route.Spec.ParentRefs[0].SectionName == nil || string(*route.Spec.ParentRefs[0].SectionName) != "http" {
		t.Fatalf("parent section = %#v", route.Spec.ParentRefs[0].SectionName)
	}
	if got := route.Spec.Hostnames[0]; got != gatewayv1.Hostname("adventure.zzrr.io") {
		t.Fatalf("hostname = %q", got)
	}
	if got := *route.Spec.Rules[0].Matches[0].Path.Type; got != gatewayv1.PathMatchPathPrefix {
		t.Fatalf("path type = %q", got)
	}
	if got := *route.Spec.Rules[0].Matches[0].Path.Value; got != "/serverlogin" {
		t.Fatalf("path = %q", got)
	}
	if got := string(route.Spec.Rules[0].BackendRefs[0].Name); got != "logingame-blue" {
		t.Fatalf("backend name = %q", got)
	}
	if got := *route.Spec.Rules[0].BackendRefs[0].Port; got != gatewayv1.PortNumber(9020) {
		t.Fatalf("backend port = %d", got)
	}
}

func TestDeleteExtraHTTPRoute(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := gatewayv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := zzrrv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-gateway-blue", Namespace: "adventure"},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	mgr := NewHTTPRouteManager(k8sClient, scheme)

	if err := mgr.DeleteExtraHTTPRoute(ctx, extraHTTPRouteTestGameService()); err != nil {
		t.Fatalf("DeleteExtraHTTPRoute() error = %v", err)
	}

	err := k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: "adventure"}, &gatewayv1.HTTPRoute{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func extraHTTPRouteTestGameService() *zzrrv1alpha1.GameService {
	return &zzrrv1alpha1.GameService{
		ObjectMeta: metav1.ObjectMeta{Name: "blue", Namespace: "control"},
		Spec: zzrrv1alpha1.GameServiceSpec{
			Route: zzrrv1alpha1.RouteConfig{
				Host:       "adventure.zzrr.io",
				PathType:   "Prefix",
				PathPrefix: "/connector",
				Port:       3010,
			},
			Gateway: &zzrrv1alpha1.GatewayConfig{
				ParentRef: zzrrv1alpha1.GatewayParentRef{
					Name:        "shared-gateway",
					Namespace:   "gateway-system",
					SectionName: "http",
				},
			},
			ConnectorNamespace: "adventure",
			DeployGroup:       zzrrv1alpha1.DeployGroupConfig{Role: "blue", Active: true},
			ExtraIngress: &zzrrv1alpha1.ExtraIngressConfig{
				Name:        "gamelogin",
				Annotations: map[string]string{"extra-route": "true"},
				Paths: []zzrrv1alpha1.ExtraIngressPath{
					{PathType: "Prefix", Path: "/serverlogin", ServiceName: "logingame-blue", Port: 9020},
				},
			},
		},
	}
}
```

- [ ] **Step 2: 运行 manager 测试确认失败**

Run:

```bash
go test ./internal/controller -run 'TestReconcileExtraHTTPRoute|TestDeleteExtraHTTPRoute' -count=1
```

Expected: FAIL，错误包含：

```text
undefined: (*HTTPRouteManager).ReconcileExtraHTTPRoute
undefined: (*HTTPRouteManager).DeleteExtraHTTPRoute
```

- [ ] **Step 3: 实现 `ReconcileExtraHTTPRoute` 和 `DeleteExtraHTTPRoute`**

在 `internal/controller/httproute_manager.go` 末尾增加：

```go
func (m *HTTPRouteManager) ReconcileExtraHTTPRoute(ctx context.Context, gs *zzrrv1alpha1.GameService) error {
	log := log.FromContext(ctx)
	if gs.Spec.ExtraIngress == nil {
		return nil
	}
	if gs.Spec.Gateway == nil {
		return fmt.Errorf("gateway config is required when reconciling extra httproute")
	}

	httpRouteName := extraTrafficName(gs, zzrrv1alpha1.TrafficModeGateway)
	rules := make([]gatewayv1.HTTPRouteRule, 0, len(gs.Spec.ExtraIngress.Paths))
	for _, configuredPath := range gs.Spec.ExtraIngress.Paths {
		pathType := extraHTTPPathType(configuredPath.PathType)
		path := configuredPath.Path
		svcName := gatewayv1.ObjectName(configuredPath.ServiceName)
		port := gatewayv1.PortNumber(configuredPath.Port)
		rules = append(rules, gatewayv1.HTTPRouteRule{
			Matches: []gatewayv1.HTTPRouteMatch{
				{
					Path: &gatewayv1.HTTPPathMatch{
						Type:  &pathType,
						Value: &path,
					},
				},
			},
			BackendRefs: []gatewayv1.HTTPBackendRef{
				{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: svcName,
							Port: &port,
						},
					},
				},
			},
		})
	}

	parentRef := gatewayv1.ParentReference{
		Name: gatewayv1.ObjectName(gs.Spec.Gateway.ParentRef.Name),
	}
	if gs.Spec.Gateway.ParentRef.Namespace != "" {
		namespace := gatewayv1.Namespace(gs.Spec.Gateway.ParentRef.Namespace)
		parentRef.Namespace = &namespace
	}
	if gs.Spec.Gateway.ParentRef.SectionName != "" {
		sectionName := gatewayv1.SectionName(gs.Spec.Gateway.ParentRef.SectionName)
		parentRef.SectionName = &sectionName
	}

	desiredHTTPRoute := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        httpRouteName,
			Namespace:   gs.Spec.ConnectorNamespace,
			Labels:      extraTrafficLabels(gs.Spec.DeployGroup.Role),
			Annotations: maps.Clone(gs.Spec.ExtraIngress.Annotations),
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{parentRef},
			},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(gs.Spec.Route.Host)},
			Rules:     rules,
		},
	}

	if gs.Namespace == gs.Spec.ConnectorNamespace {
		if err := controllerutil.SetControllerReference(gs, desiredHTTPRoute, m.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
	}

	var existingHTTPRoute gatewayv1.HTTPRoute
	if err := m.Get(ctx, client.ObjectKey{Name: httpRouteName, Namespace: gs.Spec.ConnectorNamespace}, &existingHTTPRoute); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get extra httproute: %w", err)
		}
		if err := m.Create(ctx, desiredHTTPRoute); err != nil {
			return fmt.Errorf("failed to create extra httproute: %w", err)
		}
		log.Info("Created extra HTTPRoute", "httproute", httpRouteName)
		return nil
	}

	existingHTTPRoute.Spec = desiredHTTPRoute.Spec
	existingHTTPRoute.Annotations = maps.Clone(desiredHTTPRoute.Annotations)
	existingHTTPRoute.Labels = maps.Clone(desiredHTTPRoute.Labels)
	if err := m.Update(ctx, &existingHTTPRoute); err != nil {
		return fmt.Errorf("failed to update extra httproute: %w", err)
	}

	log.Info("Updated extra HTTPRoute", "httproute", httpRouteName, "paths", len(rules))
	return nil
}

func (m *HTTPRouteManager) DeleteExtraHTTPRoute(ctx context.Context, gs *zzrrv1alpha1.GameService) error {
	if gs.Spec.ExtraIngress == nil {
		return nil
	}
	routeName := extraTrafficName(gs, zzrrv1alpha1.TrafficModeGateway)
	var route gatewayv1.HTTPRoute
	if err := m.Get(ctx, client.ObjectKey{Name: routeName, Namespace: gs.Spec.ConnectorNamespace}, &route); err != nil {
		if apimeta.IsNoMatchError(err) {
			return nil
		}
		return client.IgnoreNotFound(err)
	}
	return m.Delete(ctx, &route)
}
```

- [ ] **Step 4: 运行 HTTPRoute manager 测试确认通过**

Run:

```bash
go test ./internal/controller -run 'TestReconcileExtraHTTPRoute|TestDeleteExtraHTTPRoute' -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交 HTTPRoute manager 方法**

Run:

```bash
git add internal/controller/httproute_manager.go internal/controller/httproute_manager_extra_test.go
git commit -m "feat: add extra httproute manager"
```

Expected: commit 成功。

## Task 5: Reconciler 集成与切换语义

**Files:**
- Modify: `internal/controller/gameservice_controller.go:138-190`
- Modify: `internal/controller/gameservice_controller.go:300-317`
- Modify: `internal/controller/gameservice_controller_test.go`

- [ ] **Step 1: 增加 controller 测试 helper**

在 `internal/controller/gameservice_controller_test.go` 的 `createConnectorPod` helper 后增加：

```go
	addExtraIngress := func(gs *zzrrv1alpha1.GameService) {
		gs.Spec.Ingress.Annotations = map[string]string{
			"main-only": "true",
		}
		gs.Spec.Ingress.TLS = &zzrrv1alpha1.TLSConfig{
			SecretName: "game-tls",
		}
		gs.Spec.ExtraIngress = &zzrrv1alpha1.ExtraIngressConfig{
			Name: "gamelogin",
			Annotations: map[string]string{
				"extra-only": "true",
			},
			Paths: []zzrrv1alpha1.ExtraIngressPath{
				{
					PathType:    "Prefix",
					Path:        "/serverlogin",
					ServiceName: "logingame-blue",
					Port:        9020,
				},
			},
		}
		Expect(k8sClient.Update(ctx, gs)).To(Succeed())
	}

	addGatewayExtraIngress := func(gs *zzrrv1alpha1.GameService) {
		gs.Spec.ExtraIngress = &zzrrv1alpha1.ExtraIngressConfig{
			Name: "gamelogin",
			Annotations: map[string]string{
				"extra-route": "true",
			},
			Paths: []zzrrv1alpha1.ExtraIngressPath{
				{
					PathType:    "Prefix",
					Path:        "/serverlogin",
					ServiceName: "logingame-blue",
					Port:        9020,
				},
			},
		}
		Expect(k8sClient.Update(ctx, gs)).To(Succeed())
	}
```

- [ ] **Step 2: 增加 active Ingress 和 Gateway 创建测试**

在 `Context("When trafficMode is omitted"` 后增加：

```go
		Context("When extra ingress is configured for Ingress traffic", func() {
			It("should create an extra Ingress for active groups", func() {
				gs := createGS(true)
				addExtraIngress(gs)
				createConnectorPod("connector-0")
				reconcileOnce()
				reconcileOnce()

				ing := &networkingv1.Ingress{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: testNs}, ing)).To(Succeed())
				Expect(ing.Spec.IngressClassName).NotTo(BeNil())
				Expect(*ing.Spec.IngressClassName).To(Equal("nginx"))
				Expect(ing.Spec.Rules).To(HaveLen(1))
				Expect(ing.Spec.Rules[0].Host).To(Equal("test.example.com"))
				Expect(ing.Spec.Rules[0].HTTP.Paths).To(HaveLen(1))
				Expect(ing.Spec.Rules[0].HTTP.Paths[0].Path).To(Equal("/serverlogin"))
				Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name).To(Equal("logingame-blue"))
				Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number).To(Equal(int32(9020)))
				Expect(ing.Annotations).To(HaveKeyWithValue("extra-only", "true"))
				Expect(ing.Annotations).NotTo(HaveKey("main-only"))
				Expect(ing.Spec.TLS).To(HaveLen(1))
				Expect(ing.Spec.TLS[0].Hosts).To(Equal([]string{"test.example.com"}))
				Expect(ing.Spec.TLS[0].SecretName).To(Equal("game-tls"))
			})
		})
```

在 `Context("When trafficMode is Gateway"` 中增加：

```go
			It("should create an extra HTTPRoute for active groups", func() {
				gs := createGatewayGS(true)
				addGatewayExtraIngress(gs)
				createConnectorPod("connector-0")
				reconcileOnce()
				reconcileOnce()

				route := &gatewayv1.HTTPRoute{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: testNs}, route)).To(Succeed())
				Expect(route.Annotations).To(HaveKeyWithValue("extra-route", "true"))
				Expect(route.Spec.ParentRefs).To(HaveLen(1))
				Expect(string(route.Spec.ParentRefs[0].Name)).To(Equal("shared-gateway"))
				Expect(route.Spec.ParentRefs[0].Namespace).NotTo(BeNil())
				Expect(string(*route.Spec.ParentRefs[0].Namespace)).To(Equal("gateway-system"))
				Expect(route.Spec.ParentRefs[0].SectionName).NotTo(BeNil())
				Expect(string(*route.Spec.ParentRefs[0].SectionName)).To(Equal("http"))
				Expect(route.Spec.Hostnames).To(Equal([]gatewayv1.Hostname{"test.example.com"}))
				Expect(route.Spec.Rules).To(HaveLen(1))
				Expect(route.Spec.Rules[0].Matches).To(HaveLen(1))
				Expect(*route.Spec.Rules[0].Matches[0].Path.Type).To(Equal(gatewayv1.PathMatchPathPrefix))
				Expect(*route.Spec.Rules[0].Matches[0].Path.Value).To(Equal("/serverlogin"))
				Expect(route.Spec.Rules[0].BackendRefs).To(HaveLen(1))
				Expect(string(route.Spec.Rules[0].BackendRefs[0].Name)).To(Equal("logingame-blue"))
				Expect(*route.Spec.Rules[0].BackendRefs[0].Port).To(Equal(gatewayv1.PortNumber(9020)))
			})
```

- [ ] **Step 3: 增加 active 模式切换清理测试**

在 `Context("When extra ingress is configured for Ingress traffic"` 中增加：

```go
			It("should delete stale extra HTTPRoute when active traffic mode is Ingress", func() {
				gs := createGS(true)
				addExtraIngress(gs)
				staleRoute := &gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gamelogin-gateway-blue",
						Namespace: testNs,
					},
				}
				Expect(k8sClient.Create(ctx, staleRoute)).To(Succeed())
				createConnectorPod("connector-0")
				reconcileOnce()
				reconcileOnce()

				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: testNs}, staleRoute))).To(BeTrue())
			})
```

在 `Context("When trafficMode is Gateway"` 中增加：

```go
			It("should delete stale extra Ingress when active traffic mode is Gateway", func() {
				gs := createGatewayGS(true)
				addGatewayExtraIngress(gs)
				staleIngress := &networkingv1.Ingress{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gamelogin-ingress-blue",
						Namespace: testNs,
					},
				}
				Expect(k8sClient.Create(ctx, staleIngress)).To(Succeed())
				createConnectorPod("connector-0")
				reconcileOnce()
				reconcileOnce()

				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: testNs}, staleIngress))).To(BeTrue())
			})
```

- [ ] **Step 4: 增加 standby 删除自身 role 资源测试**

在 `Context("When switching from active to inactive"` 中增加：

```go
			It("should delete extra traffic resources for the standby role only", func() {
				gs := createGS(false)
				addExtraIngress(gs)
				blueIngress := &networkingv1.Ingress{
					ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-ingress-blue", Namespace: testNs},
				}
				blueRoute := &gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-gateway-blue", Namespace: testNs},
				}
				greenIngress := &networkingv1.Ingress{
					ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-ingress-green", Namespace: testNs},
				}
				Expect(k8sClient.Create(ctx, blueIngress)).To(Succeed())
				Expect(k8sClient.Create(ctx, blueRoute)).To(Succeed())
				Expect(k8sClient.Create(ctx, greenIngress)).To(Succeed())

				reconcileOnce()
				reconcileOnce()

				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: testNs}, blueIngress))).To(BeTrue())
				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: testNs}, blueRoute))).To(BeTrue())
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-green", Namespace: testNs}, greenIngress)).To(Succeed())
			})
```

- [ ] **Step 5: 运行 focused 测试确认失败**

Run:

```bash
go test ./internal/controller -run TestControllers -ginkgo.focus "extra|stale|standby role" -count=1
```

Expected: FAIL，active extra traffic 不会创建，stale resources 不会删除，standby extra traffic 不会删除。

- [ ] **Step 6: 接入 active Ingress 分支**

在 `internal/controller/gameservice_controller.go` 的 Ingress 分支中，紧跟主 `ingMgr.ReconcileIngress(ctx, &gs, ordinals)` 成功后加入：

```go
				if err := routeMgr.DeleteExtraHTTPRoute(ctx, &gs); err != nil {
					log.Error(err, "Failed to delete stale extra HTTPRoute for ingress mode")
					r.Recorder.Event(&gs, corev1.EventTypeWarning, "ExtraTrafficDeleteFailed", err.Error())
					r.setCondition(&gs, "Available", metav1.ConditionFalse, "ExtraTrafficDeleteFailed", err.Error())
					_ = r.Status().Update(ctx, &gs)
					return ctrl.Result{}, err
				}
				if err := ingMgr.ReconcileExtraIngress(ctx, &gs); err != nil {
					log.Error(err, "Failed to reconcile extra Ingress")
					r.Recorder.Event(&gs, corev1.EventTypeWarning, "ExtraIngressReconcileFailed", err.Error())
					r.setCondition(&gs, "Available", metav1.ConditionFalse, "ExtraIngressReconcileFailed", err.Error())
					_ = r.Status().Update(ctx, &gs)
					return ctrl.Result{}, err
				}
```

- [ ] **Step 7: 接入 active Gateway 分支**

在 Gateway 分支中，紧跟主 `routeMgr.ReconcileHTTPRoute(ctx, &gs, ordinals)` 成功后加入：

```go
				if err := ingMgr.DeleteExtraIngress(ctx, &gs); err != nil {
					log.Error(err, "Failed to delete stale extra Ingress for gateway mode")
					r.Recorder.Event(&gs, corev1.EventTypeWarning, "ExtraTrafficDeleteFailed", err.Error())
					r.setCondition(&gs, "Available", metav1.ConditionFalse, "ExtraTrafficDeleteFailed", err.Error())
					_ = r.Status().Update(ctx, &gs)
					return ctrl.Result{}, err
				}
				if err := routeMgr.ReconcileExtraHTTPRoute(ctx, &gs); err != nil {
					log.Error(err, "Failed to reconcile extra HTTPRoute")
					r.Recorder.Event(&gs, corev1.EventTypeWarning, "ExtraHTTPRouteReconcileFailed", err.Error())
					r.setCondition(&gs, "Available", metav1.ConditionFalse, "ExtraHTTPRouteReconcileFailed", err.Error())
					_ = r.Status().Update(ctx, &gs)
					return ctrl.Result{}, err
				}
```

- [ ] **Step 8: 接入 standby 分支**

在 standby 分支中，主 Ingress 和 HTTPRoute 删除后加入：

```go
			if err := ingMgr.DeleteExtraIngress(ctx, &gs); err != nil {
				log.Error(err, "Failed to delete extra Ingress for standby group")
				r.Recorder.Event(&gs, corev1.EventTypeWarning, "ExtraTrafficDeleteFailed", err.Error())
				r.setCondition(&gs, "Available", metav1.ConditionFalse, "ExtraTrafficDeleteFailed", err.Error())
			}
			if err := routeMgr.DeleteExtraHTTPRoute(ctx, &gs); err != nil {
				log.Error(err, "Failed to delete extra HTTPRoute for standby group")
				r.Recorder.Event(&gs, corev1.EventTypeWarning, "ExtraTrafficDeleteFailed", err.Error())
				r.setCondition(&gs, "Available", metav1.ConditionFalse, "ExtraTrafficDeleteFailed", err.Error())
			}
```

- [ ] **Step 9: 接入 finalizer 分支**

在 `finalize` 中主 `DeleteIngress` 和 `DeleteHTTPRoute` 成功后加入：

```go
	if err := ingMgr.DeleteExtraIngress(ctx, gs); err != nil {
		log.Error(err, "Failed to delete extra Ingress during finalization")
		return ctrl.Result{}, err
	}
	if err := routeMgr.DeleteExtraHTTPRoute(ctx, gs); err != nil {
		log.Error(err, "Failed to delete extra HTTPRoute during finalization")
		return ctrl.Result{}, err
	}
```

- [ ] **Step 10: 运行 controller focused 测试确认通过**

Run:

```bash
go test ./internal/controller -run TestControllers -ginkgo.focus "extra|stale|standby role" -count=1
```

Expected: PASS。

- [ ] **Step 11: 运行 controller 全量测试确认通过**

Run:

```bash
go test ./internal/controller -count=1
```

Expected: PASS。

- [ ] **Step 12: 提交 reconciler 集成**

Run:

```bash
git add internal/controller/gameservice_controller.go internal/controller/gameservice_controller_test.go
git commit -m "feat: reconcile extra traffic entries"
```

Expected: commit 成功。

## Task 6: 样例、生成物和全量验证

**Files:**
- Modify: `config/samples/zzrr_v1alpha1_gameservice.yaml`
- Modify: `config/samples/zzrr_v1alpha1_gameservice_gateway.yaml`
- Generated: `api/v1alpha1/zz_generated.deepcopy.go`
- Generated: `config/crd/bases/gs.zzrr.io_gameservices.yaml`

- [ ] **Step 1: 更新 Ingress 样例**

在 `config/samples/zzrr_v1alpha1_gameservice.yaml` 的 `ingress` 块后加入：

```yaml
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
```

- [ ] **Step 2: 更新 Gateway 样例**

在 `config/samples/zzrr_v1alpha1_gameservice_gateway.yaml` 的 `gateway` 块后加入：

```yaml
  extraIngress:
    name: gamelogin
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
```

- [ ] **Step 3: 重新生成 manifests 和 deepcopy**

Run:

```bash
make manifests
make generate
```

Expected: PASS，并保持生成物与 API 类型一致。

- [ ] **Step 4: 运行代码格式化和测试**

Run:

```bash
make lint-fix
make test
```

Expected: PASS。若 `make test` 因 envtest 二进制下载或网络失败，按当前环境代理提示使用 `127.0.0.1:7897` 后重试。

- [ ] **Step 5: 检查最终 diff**

Run:

```bash
git status --short
git diff --stat
```

Expected: 只包含本需求相关文件。未跟踪的 `.DS_Store`、`docs/risk-review-and-fix-plan.md`、`ip.yaml` 保持未暂存。

- [ ] **Step 6: 提交样例和生成物**

Run:

```bash
git add api/v1alpha1/zz_generated.deepcopy.go config/crd/bases/gs.zzrr.io_gameservices.yaml config/samples/zzrr_v1alpha1_gameservice.yaml config/samples/zzrr_v1alpha1_gameservice_gateway.yaml
git commit -m "chore: refresh extra traffic manifests"
```

Expected: commit 成功。

## 自检清单

- API 覆盖：`spec.extraIngress.name`、`annotations`、`paths[].pathType`、`paths[].path`、`paths[].serviceName`、`paths[].port` 都有字段和 CRD schema。
- Ingress 覆盖：Ingress 模式使用 `gamelogin-ingress-blue` 这类资源名，host 来自 `spec.route.host`，class/TLS 来自 `spec.ingress`，后端 Service 直接使用 `serviceName`。
- Gateway 覆盖：Gateway 模式使用 `gamelogin-gateway-blue` 这类资源名，ParentRef 来自 `spec.gateway.parentRef`，Hostnames 来自 `spec.route.host`，BackendRef name 直接使用 `serviceName`。
- 切换覆盖：active 时清理当前 role 下另一个 trafficMode 的附加入口；standby 和 finalizer 删除当前 role 的附加 Ingress 和 HTTPRoute。
- 非目标覆盖：operator 不创建、不生成、不拼接附加入口后端 Service 名称。
- 回归覆盖：未配置 `spec.extraIngress` 时现有测试仍通过。
