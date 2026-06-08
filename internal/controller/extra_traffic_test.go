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
			DeployGroup:  zzrrv1alpha1.DeployGroupConfig{Role: "blue"},
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
