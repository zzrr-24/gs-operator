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

func (m *IngressManager) BuildConnectorOrdinals(podNames []string) []string {
	ordinals := make(map[string]bool, len(podNames))
	for _, name := range podNames {
		ord := GetPodOrdinal(name)
		if ord != "" {
			ordinals[ord] = true
		}
	}
	result := make([]string, 0, len(ordinals))
	for ord := range ordinals {
		result = append(result, ord)
	}
	sort.Strings(result)
	return result
}

func (m *IngressManager) ReconcileIngress(ctx context.Context, gs *zzrrv1alpha1.GameService, ordinals []string) error {
	log := log.FromContext(ctx)
	if len(ordinals) == 0 {
		log.Info("No connector pods, skipping ingress reconcile")
		return nil
	}
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
			Namespace: gs.Spec.ConnectorNamespace,
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

	// Only set owner reference when CR and Ingress are in the same namespace
	if gs.Namespace == gs.Spec.ConnectorNamespace {
		if err := controllerutil.SetControllerReference(gs, desiredIngress, m.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
	}
	// Cross-namespace owner references are disallowed by K8s.
	// We rely on labels for identifying and managing cross-namespace Ingresses.

	var existingIngress networkingv1.Ingress
	if err := m.Get(ctx, client.ObjectKey{Name: ingressName, Namespace: gs.Spec.ConnectorNamespace}, &existingIngress); err != nil {
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
	if err := m.Get(ctx, client.ObjectKey{Name: ingressName, Namespace: gs.Spec.ConnectorNamespace}, &ing); err != nil {
		return client.IgnoreNotFound(err)
	}
	return m.Delete(ctx, &ing)
}
