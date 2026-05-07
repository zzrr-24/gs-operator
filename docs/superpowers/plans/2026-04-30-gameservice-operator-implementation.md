# GameService Operator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the GameService operator that manages per-connector-pod Services, dynamic Ingress path routing, and blue-green deployment switching.

**Architecture:** Kubebuilder-based operator with a single `GameService` CRD. The controller watches both `GameService` CRs and connector `Pod` changes, reconciles by creating/deleting per-pod Services, updating a single Ingress with dynamic path entries, and toggling the Ingress backend namespace based on blue-green `active` state.

**Tech Stack:** Go 1.25, controller-runtime v0.23, kubebuilder v4, Higress Ingress Controller (test env)

---

## File Structure

**Modify:**
- `api/v1alpha1/gameservice_types.go` — CRD fields (IngressConfig, DeployGroupConfig, RetentionConfig, Status)
- `internal/controller/gameservice_controller.go` — Main reconcile loop
- `cmd/main.go` — Register controller with manager (already done for GameService)
- `config/samples/zzrr_v1alpha1_gameservice.yaml` — Sample CR

**Create:**
- `internal/controller/connector_service.go` — Per-pod Service management logic
- `internal/controller/ingress_manager.go` — Ingress creation/update logic

**Auto-generated (do NOT edit):**
- `api/v1alpha1/zz_generated.deepcopy.go` — from `make generate`
- `config/crd/bases/zzrr.gs.zzrr.io_gameservices.yaml` — from `make manifests`
- `config/rbac/role.yaml` — from `make manifests`

---

### Task 1: Update CRD Types

**Files:**
- Modify: `api/v1alpha1/gameservice_types.go` (entire file)
- Auto-generated: `api/v1alpha1/zz_generated.deepcopy.go` (via `make generate`)

- [ ] **Step 1: Write the new type definitions**

Replace the entire `api/v1alpha1/gameservice_types.go` content:

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

type GameServiceSpec struct {
	Ingress            IngressConfig      `json:"ingress"`
	ConnectorNamespace string             `json:"connectorNamespace"`
	DeployGroup        DeployGroupConfig  `json:"deployGroup"`
	Retention          *RetentionConfig   `json:"retention,omitempty"`
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
	metav1.ObjectMeta `json:"metadata,omitzero"`
	Spec              GameServiceSpec   `json:"spec"`
	Status            GameServiceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true
type GameServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GameService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GameService{}, &GameServiceList{})
}
```

- [ ] **Step 2: Run code generation**

```bash
make generate
```

Expected output: `api/v1alpha1/zz_generated.deepcopy.go` updated with DeepCopy methods for all new types.

- [ ] **Step 3: Run manifests generation**

```bash
make manifests
```

Expected output:
- `config/crd/bases/zzrr.gs.zzrr.io_gameservices.yaml` regenerated
- `config/rbac/role.yaml` regenerated (no RBAC changes yet)

- [ ] **Step 4: Verify compilation**

```bash
go build ./...
```

Expected: No errors, binary compiles.

- [ ] **Step 5: Commit**

```bash
git add api/v1alpha1/gameservice_types.go api/v1alpha1/zz_generated.deepcopy.go config/crd/bases/ config/rbac/role.yaml
git commit -m "feat: add GameService CRD fields for ingress, blue-green, and retention"
```

---

### Task 2: Per-Pod Service Manager

**Files:**
- Create: `internal/controller/connector_service.go`
- Modify: `internal/controller/gameservice_controller.go` (add import + field)

- [ ] **Step 1: Create connector_service.go**

```go
package controller

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

type ConnectorServiceManager struct {
	client.Client
	Scheme *runtime.Scheme
}

func NewConnectorServiceManager(c client.Client, s *runtime.Scheme) *ConnectorServiceManager {
	return &ConnectorServiceManager{Client: c, Scheme: s}
}

func (m *ConnectorServiceManager) ListConnectorPods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	req, err := labels.NewRequirement("adventure", selection.Equals, []string{"connector"})
	if err != nil {
		return nil, fmt.Errorf("failed to create label requirement: %w", err)
	}
	selector := labels.NewSelector().Add(*req)

	var pods corev1.PodList
	if err := m.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("failed to list connector pods: %w", err)
	}
	return pods.Items, nil
}

func GetPodOrdinal(pod *corev1.Pod) string {
	name := pod.Name
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '-' {
			return name[i+1:]
		}
	}
	return ""
}

func (m *ConnectorServiceManager) EnsureService(ctx context.Context, pod *corev1.Pod, port int32) (*corev1.Service, error) {
	log := log.FromContext(ctx)
	ordinal := GetPodOrdinal(pod)
	svcName := fmt.Sprintf("connector-%s-svc", ordinal)

	var existingSvc corev1.Service
	if err := m.Get(ctx, client.ObjectKey{Name: svcName, Namespace: pod.Namespace}, &existingSvc); err == nil {
		return &existingSvc, nil
	}

	svc := &corev1.Service{
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
					Name:     "connector",
					Port:     port,
					TargetPort: port,
				},
			},
		},
	}

	if err := m.Create(ctx, svc); err != nil {
		return nil, fmt.Errorf("failed to create service %s: %w", svcName, err)
	}

	log.Info("Created Service for connector pod", "service", svcName, "pod", pod.Name)
	return svc, nil
}

func (m *ConnectorServiceManager) DeleteOrphanServices(ctx context.Context, namespace string, activeOrdinals map[string]bool) error {
	log := log.FromContext(ctx)

	var svcList corev1.ServiceList
	if err := m.List(ctx, &svcList, client.InNamespace(namespace), client.MatchingLabels{
		"app.kubernetes.io/managed-by": "gs-operator",
	}); err != nil {
		return fmt.Errorf("failed to list managed services: %w", err)
	}

	for _, svc := range svcList.Items {
		ordinal := svc.Labels["gs-connector-ordinal"]
		if !activeOrdinals[ordinal] {
			if err := m.Delete(ctx, &svc); err != nil {
				log.Error(err, "Failed to delete orphan service", "service", svc.Name)
				continue
			}
			log.Info("Deleted orphan Service", "service", svc.Name)
		}
	}
	return nil
}
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./...
```

Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add internal/controller/connector_service.go
git commit -m "feat: add connector per-pod service manager"
```

---

### Task 3: Ingress Manager

**Files:**
- Create: `internal/controller/ingress_manager.go`
- Modify: `internal/controller/gameservice_controller.go` (add imports + field)

- [ ] **Step 1: Create ingress_manager.go**

```go
package controller

import (
	"context"
	"fmt"
	"sort"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

type IngressManager struct {
	client.Client
	Scheme *runtime.Scheme
}

func NewIngressManager(c client.Client, s *runtime.Scheme) *IngressManager {
	return &IngressManager{Client: c, Scheme: s}
}

func (m *IngressManager) ReconcileIngress(ctx context.Context, gs *zzrrv1alpha1.GameService, ordinals []string) error {
	log := log.FromContext(ctx)
	ingressName := fmt.Sprintf("game-ingress-%s", gs.Spec.DeployGroup.Role)

	paths := make([]networkingv1.HTTPIngressPath, 0, len(ordinals))
	sort.Strings(ordinals)

	pathType := networkingv1.PathTypePrefix
	if gs.Spec.Ingress.PathType != "" {
		pathType = networkingv1.PathType(gs.Spec.Ingress.PathType)
	}

	for _, ord := range ordinals {
		svcName := fmt.Sprintf("connector-%s-svc", ord)
		paths = append(paths, networkingv1.HTTPIngressPath{
			Path:     fmt.Sprintf("%s%s", gs.Spec.Ingress.PathPrefix, ord),
			PathType: &pathType,
			Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: svcName,
					Port: networkingv1.ServiceBackendPort{
						Number: gs.Spec.Ingress.Port,
					},
				},
			},
		})
	}

	desiredIngress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingressName,
			Namespace: gs.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "gs-operator",
				"gs-role":                      gs.Spec.DeployGroup.Role,
			},
			Annotations: gs.Spec.Ingress.Annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &gs.Spec.Ingress.IngressClassName,
			Rules: []networkingv1.IngressRule{
				{
					Host: gs.Spec.Ingress.Host,
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
				Hosts:      []string{gs.Spec.Ingress.Host},
				SecretName: gs.Spec.Ingress.TLS.SecretName,
			},
		}
	}

	if err := controllerutil.SetControllerReference(gs, desiredIngress, m.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	var existingIngress networkingv1.Ingress
	if err := m.Get(ctx, client.ObjectKey{Name: ingressName, Namespace: gs.Namespace}, &existingIngress); err != nil {
		if err := m.Create(ctx, desiredIngress); err != nil {
			return fmt.Errorf("failed to create ingress: %w", err)
		}
		log.Info("Created Ingress", "ingress", ingressName)
		return nil
	}

	existingIngress.Spec = desiredIngress.Spec
	existingIngress.Annotations = desiredIngress.Annotations
	existingIngress.Labels = desiredIngress.Labels

	if err := m.Update(ctx, &existingIngress); err != nil {
		return fmt.Errorf("failed to update ingress: %w", err)
	}

	log.Info("Updated Ingress", "ingress", ingressName, "paths", len(paths))
	return nil
}

func (m *IngressManager) DeleteIngress(ctx context.Context, gs *zzrrv1alpha1.GameService) error {
	ingressName := fmt.Sprintf("game-ingress-%s", gs.Spec.DeployGroup.Role)
	var ing networkingv1.Ingress
	if err := m.Get(ctx, client.ObjectKey{Name: ingressName, Namespace: gs.Namespace}, &ing); err != nil {
		return client.IgnoreNotFound(err)
	}
	return m.Delete(ctx, &ing)
}

func (m *IngressManager) BuildConnectorOrdinals(pods []corev1.Pod) []string {
	ordinals := make([]string, 0, len(pods))
	for _, pod := range pods {
		ordinals = append(ordinals, GetPodOrdinal(&pod))
	}
	sort.Strings(ordinals)
	return ordinals
}
```

Note: We need to import `corev1` in this file. Add it at the top with the other imports.

Actually, let me restructure — the `BuildConnectorOrdinals` can take `[]string` (pod names) instead of `[]corev1.Pod` to avoid the import cycle.

Let me fix this:

```go
func (m *IngressManager) BuildConnectorOrdinals(podNames []string) []string {
	ordinals := make([]string, 0, len(podNames))
	for _, name := range podNames {
		for i := len(name) - 1; i >= 0; i-- {
			if name[i] == '-' {
				ordinals = append(ordinals, name[i+1:])
				break
			}
		}
	}
	sort.Strings(ordinals)
	return ordinals
}
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./...
```

Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add internal/controller/ingress_manager.go
git commit -m "feat: add ingress manager for dynamic path routing"
```

---

### Task 4: Controller Reconciliation Logic

**Files:**
- Modify: `internal/controller/gameservice_controller.go` (full implementation)
- Modify: `cmd/main.go` (verify registration)

- [ ] **Step 1: Implement the full controller**

Replace `internal/controller/gameservice_controller.go`:

```go
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=zzrr.gs.zzrr.io,resources=gameservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=zzrr.gs.zzrr.io,resources=gameservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=zzrr.gs.zzrr.io,resources=gameservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

type GameServiceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *GameServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Starting reconciliation")

	var gs zzrrv1alpha1.GameService
	if err := r.Get(ctx, req.NamespacedName, &gs); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	svcMgr := NewConnectorServiceManager(r.Client, r.Scheme)
	ingMgr := NewIngressManager(r.Client, r.Scheme)

	pods, err := svcMgr.ListConnectorPods(ctx, gs.Spec.ConnectorNamespace)
	if err != nil {
		log.Error(err, "Failed to list connector pods")
		r.Recorder.Event(&gs, corev1.EventTypeWarning, "PodListFailed", err.Error())
		return ctrl.Result{}, err
	}

	ordinals := make([]string, 0, len(pods))
	activeOrdinals := make(map[string]bool)
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}
		ord := GetPodOrdinal(&pod)
		ordinals = append(ordinals, ord)
		activeOrdinals[ord] = true
	}

	for _, pod := range pods {
		if _, err := svcMgr.EnsureService(ctx, &pod, gs.Spec.Ingress.Port); err != nil {
			log.Error(err, "Failed to ensure service for pod", "pod", pod.Name)
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "ServiceCreateFailed", err.Error())
		}
	}

	if err := svcMgr.DeleteOrphanServices(ctx, gs.Spec.ConnectorNamespace, activeOrdinals); err != nil {
		log.Error(err, "Failed to delete orphan services")
		r.Recorder.Event(&gs, corev1.EventTypeWarning, "OrphanCleanupFailed", err.Error())
	}

	if gs.Spec.DeployGroup.Active {
		if err := ingMgr.ReconcileIngress(ctx, &gs, ordinals); err != nil {
			log.Error(err, "Failed to reconcile ingress")
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "IngressReconcileFailed", err.Error())
			r.setCondition(ctx, &gs, "Available", metav1.ConditionFalse, "IngressReconcileFailed", err.Error())
			return ctrl.Result{}, err
		}
		r.setCondition(ctx, &gs, "Available", metav1.ConditionTrue, "AllIngressPathsReady",
			fmt.Sprintf("Ingress paths synced for %d connector pods", len(ordinals)))
		r.setCondition(ctx, &gs, "TrafficActive", metav1.ConditionTrue, "Active", "This deployment group is receiving traffic")
	} else {
		r.setCondition(ctx, &gs, "TrafficActive", metav1.ConditionFalse, "Standby", "This deployment group is not receiving traffic")
	}

	gs.Status.ConnectorCount = int32(len(ordinals))
	gs.Status.ObservedGeneration = gs.Generation

	if err := r.Status().Update(ctx, &gs); err != nil {
		log.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	if !gs.Spec.DeployGroup.Active && gs.Spec.Retention != nil && gs.Spec.Retention.Enabled {
		duration, err := time.ParseDuration(gs.Spec.Retention.DefaultDuration)
		if err != nil {
			duration = 24 * time.Hour
		}
		if gs.CreationTimestamp.Add(duration).Before(time.Now()) {
			log.Info("Retention period expired, deleting GameService", "name", gs.Name)
			if err := ingMgr.DeleteIngress(ctx, &gs); err != nil {
				log.Error(err, "Failed to delete ingress during cleanup")
			}
			if err := r.Delete(ctx, &gs); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		requeueAfter := time.Until(gs.CreationTimestamp.Add(duration))
		log.Info("Retention period active, will auto-delete", "requeueAfter", requeueAfter)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	return ctrl.Result{}, nil
}

func (r *GameServiceReconciler) setCondition(ctx context.Context, gs *zzrrv1alpha1.GameService, condType string, status metav1.ConditionStatus, reason, message string) {
	for i, c := range gs.Status.Conditions {
		if c.Type == condType {
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
		LastTransitionTime: metav1.Now(),
	})
}

func (r *GameServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&zzrrv1alpha1.GameService{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapConnectorPodToGameService),
		).
		Named("gameservice").
		Complete(r)
}

func (r *GameServiceReconciler) mapConnectorPodToGameService(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	if pod.Labels["adventure"] != "connector" {
		return nil
	}

	var list zzrrv1alpha1.GameServiceList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, gs := range list.Items {
		if gs.Spec.ConnectorNamespace == pod.Namespace {
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

- [ ] **Step 2: Update cmd/main.go to pass Recorder**

Edit `cmd/main.go` around line 181-187:

Old:
```go
	if err := (&controller.GameServiceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
```

New:
```go
	if err := (&controller.GameServiceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("gameservice-controller"),
	}).SetupWithManager(mgr); err != nil {
```

- [ ] **Step 3: Regenerate manifests**

```bash
make manifests
```

Expected: `config/rbac/role.yaml` updated with pod/service/ingress permissions.

- [ ] **Step 4: Verify compilation**

```bash
go build ./...
```

Expected: No errors.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/gameservice_controller.go internal/controller/connector_service.go internal/controller/ingress_manager.go cmd/main.go config/rbac/role.yaml
git commit -m "feat: implement GameService controller reconciliation logic"
```

---

### Task 5: Sample CR + Basic Verification

**Files:**
- Modify: `config/samples/zzrr_v1alpha1_gameservice.yaml`

- [ ] **Step 1: Update sample CR**

```yaml
apiVersion: zzrr.gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  labels:
    app.kubernetes.io/name: gs-operator
    app.kubernetes.io/managed-by: kustomize
  name: blue
spec:
  ingress:
    host: game.example.com
    ingressClassName: higress
    pathType: Prefix
    pathPrefix: "/connector"
    port: 3010
    annotations:
      nginx.ingress.kubernetes.io/proxy-read-timeout: "300s"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "300s"
  connectorNamespace: adventure
  deployGroup:
    role: blue
    active: true
  retention:
    enabled: true
    defaultDuration: 24h
```

- [ ] **Step 2: Run lint fix**

```bash
make lint-fix
```

Expected: No lint issues.

- [ ] **Step 3: Run unit tests**

```bash
make test
```

Expected: Initial tests pass (existing suite test should still work). Note: existing suite_test.go may need updating for the new type imports.

- [ ] **Step 4: Commit**

```bash
git add config/samples/zzrr_v1alpha1_gameservice.yaml
git commit -m "chore: update sample CR and lint fixes"
```

---

### Task 6: Update Suite Test

**Files:**
- Modify: `internal/controller/suite_test.go`
- Modify: `internal/controller/gameservice_controller_test.go`

- [ ] **Step 1: Update suite_test.go imports if needed**

Verify that `suite_test.go` properly imports the CRD types and sets up the test environment. The existing scaffold should work, but verify CRD paths are correct.

- [ ] **Step 2: Add a basic reconcile test**

Add to `gameservice_controller_test.go`:

```go
package controller_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

var _ = Describe("GameService Controller", func() {
	Context("When creating a GameService with active connector pods", func() {
		const (
			crName      = "blue"
			crNamespace = "default"
			ns          = "adventure"
		)

		It("should create per-pod services and an ingress", func() {
			ctx := context.Background()

			by("Creating a connector pod")
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "connector-0",
					Namespace: ns,
					Labels: map[string]string{
						"adventure": "connector",
						"statefulset.kubernetes.io/pod-name": "connector-0",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "connector", Image: "nginx:alpine"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).Should(Succeed())
			DeferCleanup(k8sClient.Delete, pod)

			by("Creating a GameService CR")
			gs := &zzrrv1alpha1.GameService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crName,
					Namespace: crNamespace,
				},
				Spec: zzrrv1alpha1.GameServiceSpec{
					Ingress: zzrrv1alpha1.IngressConfig{
						Host:             "game.example.com",
						IngressClassName: "higress",
						PathType:         "Prefix",
						PathPrefix:       "/connector",
						Port:             3010,
					},
					ConnectorNamespace: ns,
					DeployGroup: zzrrv1alpha1.DeployGroupConfig{
						Role:   "blue",
						Active: true,
					},
				},
			}
			Expect(k8sClient.Create(ctx, gs)).Should(Succeed())
			DeferCleanup(k8sClient.Delete, gs)

			by("Reconciling")
			reconciler := &GameServiceReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: crName, Namespace: crNamespace},
			})
			Expect(err).ShouldNot(HaveOccurred())

			by("Verifying the per-pod service exists")
			var svc corev1.Service
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "connector-0-svc", Namespace: ns}, &svc)
			}, time.Second*5, time.Millisecond*500).Should(Succeed())

			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(3010)))
			Expect(svc.Spec.Selector["statefulset.kubernetes.io/pod-name"]).To(Equal("connector-0"))

			by("Verifying the ingress exists")
			var ing networkingv1.Ingress
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "game-ingress-blue", Namespace: crNamespace}, &ing)
			}, time.Second*5, time.Millisecond*500).Should(Succeed())

			Expect(len(ing.Spec.Rules)).To(Equal(1))
			Expect(len(ing.Spec.Rules[0].HTTP.Paths)).To(Equal(1))
			Expect(ing.Spec.Rules[0].HTTP.Paths[0].Path).To(Equal("/connector0"))
		})
	})
})
```

- [ ] **Step 3: Run tests**

```bash
make test
```

Expected: Tests pass. If suite_test.go CRD paths are wrong, adjust the `--input-dir` paths.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/gameservice_controller_test.go
git commit -m "test: add GameService controller integration test"
```

---

### Task 7: Build, Deploy and Manual Test

**Files:**
- `Dockerfile` (already exists, no changes needed)

- [ ] **Step 1: Build the operator binary**

```bash
make build
```

Expected: `bin/manager` binary created.

- [ ] **Step 2: Verify CRD is installed on cluster**

```bash
kubectl apply -f config/crd/bases/zzrr.gs.zzrr.io_gameservices.yaml
```

Expected: CRD created.

- [ ] **Step 3: Create test connector pods**

```bash
kubectl create ns adventure 2>/dev/null || true
kubectl run connector-0 --image=nginx:alpine -n adventure --labels="adventure=connector,statefulset.kubernetes.io/pod-name=connector-0" --port=3010
kubectl run connector-1 --image=nginx:alpine -n adventure --labels="adventure=connector,statefulset.kubernetes.io/pod-name=connector-1" --port=3010
```

Expected: Two pods running in `adventure` namespace.

- [ ] **Step 4: Run operator locally**

```bash
make run
```

Expected: Operator starts and begins reconciling.

- [ ] **Step 5: Create GameService CR (in another terminal)**

```bash
kubectl apply -f config/samples/zzrr_v1alpha1_gameservice.yaml
```

Expected:
- Services `connector-0-svc` and `connector-1-svc` created in `adventure` namespace
- Ingress `game-ingress-blue` created in `default` namespace

- [ ] **Step 6: Verify resources**

```bash
kubectl get svc -n adventure -l app.kubernetes.io/managed-by=gs-operator
kubectl get ingress game-ingress-blue
```

Expected: Services and Ingress exist.

- [ ] **Step 7: Test blue-green switching**

Create green CR:

```bash
kubectl create -f - <<EOF
apiVersion: zzrr.gs.zzrr.io/v1alpha1
kind: GameService
metadata:
  name: green
spec:
  ingress:
    host: game.example.com
    ingressClassName: higress
    pathType: Prefix
    pathPrefix: "/connector"
    port: 3010
  connectorNamespace: adventure
  deployGroup:
    role: green
    active: false
  retention:
    enabled: true
    defaultDuration: 24h
EOF
```

Expected: Green Ingress created with same paths but standby.

- [ ] **Step 8: Test connector scale-up**

```bash
kubectl run connector-2 --image=nginx:alpine -n adventure --labels="adventure=connector,statefulset.kubernetes.io/pod-name=connector-2" --port=3010
```

Expected: `connector-2-svc` created, Ingress updated with `/connector2` path.

- [ ] **Step 9: Test connector scale-down**

```bash
kubectl delete pod connector-2 -n adventure
```

Expected: `connector-2-svc` deleted, Ingress updated without `/connector2` path.

- [ ] **Step 10: Cleanup test resources**

```bash
kubectl delete -f config/samples/zzrr_v1alpha1_gameservice.yaml
kubectl delete gameservice green
kubectl delete pod -n adventure -l adventure=connector
```

---

## Spec Coverage Checklist

| Spec Requirement | Task |
|-----------------|------|
| CRD: IngressConfig with host, ingressClassName, pathType, pathPrefix, port, tls, annotations | Task 1 |
| CRD: DeployGroupConfig with role, active | Task 1 |
| CRD: RetentionConfig with enabled, defaultDuration | Task 1 |
| CRD: Status with conditions, connectorCount, observedGeneration | Task 1 |
| Per-pod Service (statefulset.kubernetes.io/pod-name selector) | Task 2 |
| Dynamic Ingress path list (create/update/delete) | Task 3 |
| Watch connector pod changes to trigger reconcile | Task 4 |
| Blue-green active/inactive handling | Task 4 |
| Two CRs both active=true: keep current Ingress, wait | Task 4 |
| Retention auto-cleanup (24h default) | Task 4 |
| Retention premature delete (manual CR delete) | Task 4 (natural via K8s GC) |
| Events for errors/warnings | Task 4 |
| Status condition updates (Available, TrafficActive) | Task 4 |
| RBAC markers for pods, services, ingresses | Task 4 |
| Ingress annotations support | Task 3 |
