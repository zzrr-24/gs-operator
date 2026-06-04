package controller

import (
	"context"
	"fmt"
	"maps"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

type HTTPRouteManager struct {
	client.Client
	Scheme *runtime.Scheme
}

func NewHTTPRouteManager(c client.Client, s *runtime.Scheme) *HTTPRouteManager {
	return &HTTPRouteManager{Client: c, Scheme: s}
}

func (m *HTTPRouteManager) ReconcileHTTPRoute(ctx context.Context, gs *zzrrv1alpha1.GameService, ordinals []string) error {
	log := log.FromContext(ctx)
	if gs.Spec.Gateway == nil {
		return fmt.Errorf("gateway config is required when trafficMode is Gateway")
	}
	if len(ordinals) == 0 {
		log.Info("No connector pods, skipping HTTPRoute reconcile")
		return nil
	}

	httpRouteName := fmt.Sprintf("game-route-%s", gs.Spec.DeployGroup.Role)
	rules := make([]gatewayv1.HTTPRouteRule, 0, len(ordinals))
	sort.Strings(ordinals)

	pathType := gatewayv1.PathMatchPathPrefix
	if gs.Spec.Route.PathType == "Exact" {
		pathType = gatewayv1.PathMatchExact
	}

	for _, ord := range ordinals {
		svcName := gatewayv1.ObjectName(fmt.Sprintf("connector-%s-svc", ord))
		path := fmt.Sprintf("%s%s", gs.Spec.Route.PathPrefix, ord)
		port := gs.Spec.Route.Port
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
			Name:      httpRouteName,
			Namespace: gs.Spec.ConnectorNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "gs-operator",
				"gs-role":                      gs.Spec.DeployGroup.Role,
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{parentRef},
			},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(gs.Spec.Route.Host)},
			Rules:     rules,
		},
	}

	// Only set owner reference when CR and HTTPRoute are in the same namespace
	if gs.Namespace == gs.Spec.ConnectorNamespace {
		if err := controllerutil.SetControllerReference(gs, desiredHTTPRoute, m.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
	}
	// Cross-namespace owner references are disallowed by K8s.
	// We rely on labels for identifying and managing cross-namespace HTTPRoutes.

	var existingHTTPRoute gatewayv1.HTTPRoute
	if err := m.Get(ctx, client.ObjectKey{Name: httpRouteName, Namespace: gs.Spec.ConnectorNamespace}, &existingHTTPRoute); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get httproute: %w", err)
		}
		if err := m.Create(ctx, desiredHTTPRoute); err != nil {
			return fmt.Errorf("failed to create httproute: %w", err)
		}
		log.Info("Created HTTPRoute", "httproute", httpRouteName)
		return nil
	}

	existingHTTPRoute.Spec = desiredHTTPRoute.Spec

	if existingHTTPRoute.Labels == nil {
		existingHTTPRoute.Labels = make(map[string]string)
	}
	maps.Copy(existingHTTPRoute.Labels, desiredHTTPRoute.Labels)

	if err := m.Update(ctx, &existingHTTPRoute); err != nil {
		return fmt.Errorf("failed to update httproute: %w", err)
	}

	log.Info("Updated HTTPRoute", "httproute", httpRouteName, "paths", len(rules))
	return nil
}

func (m *HTTPRouteManager) DeleteHTTPRoute(ctx context.Context, gs *zzrrv1alpha1.GameService) error {
	routeName := fmt.Sprintf("game-route-%s", gs.Spec.DeployGroup.Role)
	var route gatewayv1.HTTPRoute
	if err := m.Get(ctx, client.ObjectKey{Name: routeName, Namespace: gs.Spec.ConnectorNamespace}, &route); err != nil {
		if apimeta.IsNoMatchError(err) {
			return nil
		}
		return client.IgnoreNotFound(err)
	}
	return m.Delete(ctx, &route)
}
