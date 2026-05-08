package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
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

func GetPodOrdinal(podName string) string {
	for i := len(podName) - 1; i >= 0; i-- {
		if podName[i] == '-' {
			return podName[i+1:]
		}
	}
	return ""
}

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

	return &existingSvc, nil
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
			if err := m.Delete(ctx, &svc); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete orphan service", "service", svc.Name)
				continue
			}
			log.Info("Deleted orphan Service", "service", svc.Name)
		}
	}
	return nil
}

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
