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
	if got := ing.Annotations["extra-only"]; got != testTrueValue {
		t.Fatalf("extra annotation = %q", got)
	}
	if _, exists := ing.Annotations["main-only"]; exists {
		t.Fatalf("main annotation should not be copied")
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "game-tls" {
		t.Fatalf("tls = %#v", ing.Spec.TLS)
	}
}

func TestReconcileExtraIngressMergesMetadataOnUpdate(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := zzrrv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	existing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gamelogin-ingress-blue",
			Namespace: "adventure",
			Annotations: map[string]string{
				"extra-only":            "stale",
				"platform.example/keep": testTrueValue,
			},
			Labels: map[string]string{
				"gs-role":               "stale",
				"platform.example/keep": testTrueValue,
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	mgr := NewIngressManager(k8sClient, scheme)

	if err := mgr.ReconcileExtraIngress(ctx, extraIngressTestGameService()); err != nil {
		t.Fatalf("ReconcileExtraIngress() error = %v", err)
	}

	ing := &networkingv1.Ingress{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: "adventure"}, ing); err != nil {
		t.Fatalf("failed to get extra ingress: %v", err)
	}
	if got := ing.Annotations["platform.example/keep"]; got != testTrueValue {
		t.Fatalf("platform annotation = %q", got)
	}
	if got := ing.Annotations["extra-only"]; got != testTrueValue {
		t.Fatalf("extra annotation = %q", got)
	}
	if got := ing.Labels["platform.example/keep"]; got != testTrueValue {
		t.Fatalf("platform label = %q", got)
	}
	if got := ing.Labels["gs-role"]; got != "blue" {
		t.Fatalf("role label = %q", got)
	}
	if got := ing.Labels["gs-extra-traffic"]; got != testTrueValue {
		t.Fatalf("extra traffic label = %q", got)
	}
	if got := ing.Labels["app.kubernetes.io/managed-by"]; got != testManagedByValue {
		t.Fatalf("managed-by label = %q", got)
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
				Annotations: map[string]string{"main-only": testTrueValue},
			},
			ConnectorNamespace: "adventure",
			DeployGroup:        zzrrv1alpha1.DeployGroupConfig{Role: "blue", Active: true},
			ExtraIngress: &zzrrv1alpha1.ExtraIngressConfig{
				Name:        "gamelogin",
				Annotations: map[string]string{"extra-only": testTrueValue},
				Paths: []zzrrv1alpha1.ExtraIngressPath{
					{PathType: "Prefix", Path: "/serverlogin", ServiceName: "logingame-blue", Port: 9020},
				},
			},
		},
	}
}
