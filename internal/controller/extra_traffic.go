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
