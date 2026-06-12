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

const extraRouteAnnotationValue = "true"

func TestReconcileExtraHTTPRoute(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := gatewayv1.Install(scheme); err != nil {
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
	if got := route.Annotations["extra-route"]; got != extraRouteAnnotationValue {
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

func TestReconcileExtraHTTPRouteMergesMetadataOnUpdate(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatal(err)
	}
	if err := zzrrv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gamelogin-gateway-blue",
			Namespace: "adventure",
			Annotations: map[string]string{
				"extra-route":           "stale",
				"platform.example/keep": "true",
			},
			Labels: map[string]string{
				"gs-extra-traffic":      "false",
				"platform.example/keep": "true",
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	mgr := NewHTTPRouteManager(k8sClient, scheme)

	if err := mgr.ReconcileExtraHTTPRoute(ctx, extraHTTPRouteTestGameService()); err != nil {
		t.Fatalf("ReconcileExtraHTTPRoute() error = %v", err)
	}

	route := &gatewayv1.HTTPRoute{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: "adventure"}, route); err != nil {
		t.Fatalf("failed to get extra httproute: %v", err)
	}
	if got := route.Annotations["platform.example/keep"]; got != "true" {
		t.Fatalf("platform annotation = %q", got)
	}
	if got := route.Annotations["extra-route"]; got != extraRouteAnnotationValue {
		t.Fatalf("extra annotation = %q", got)
	}
	if got := route.Labels["platform.example/keep"]; got != "true" {
		t.Fatalf("platform label = %q", got)
	}
	if got := route.Labels["gs-role"]; got != "blue" {
		t.Fatalf("role label = %q", got)
	}
	if got := route.Labels["gs-extra-traffic"]; got != "true" {
		t.Fatalf("extra traffic label = %q", got)
	}
	if got := route.Labels["app.kubernetes.io/managed-by"]; got != "gs-operator" {
		t.Fatalf("managed-by label = %q", got)
	}
}

func TestDeleteExtraHTTPRoute(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := gatewayv1.Install(scheme); err != nil {
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
			DeployGroup:        zzrrv1alpha1.DeployGroupConfig{Role: "blue", Active: true},
			ExtraIngress: &zzrrv1alpha1.ExtraIngressConfig{
				Name:        "gamelogin",
				Annotations: map[string]string{"extra-route": extraRouteAnnotationValue},
				Paths: []zzrrv1alpha1.ExtraIngressPath{
					{PathType: "Prefix", Path: "/serverlogin", ServiceName: "logingame-blue", Port: 9020},
				},
			},
		},
	}
}
