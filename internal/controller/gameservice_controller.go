package controller

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

const gameServiceFinalizer = "zzrr.gs.zzrr.io/finalizer"

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

	svcMgr := NewConnectorServiceManager(r.Client, r.Scheme)
	ingMgr := NewIngressManager(r.Client, r.Scheme)

	pods, err := svcMgr.ListConnectorPods(ctx, gs.Spec.ConnectorNamespace)
	if err != nil {
		log.Error(err, "Failed to list connector pods")
		r.Recorder.Event(&gs, corev1.EventTypeWarning, "PodListFailed", err.Error())
		return ctrl.Result{}, err
	}

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

	if err := svcMgr.DeleteOrphanServices(ctx, gs.Spec.ConnectorNamespace, activeOrdinals); err != nil {
		log.Error(err, "Failed to delete orphan services")
		r.Recorder.Event(&gs, corev1.EventTypeWarning, "OrphanCleanupFailed", err.Error())
	}

	if gs.Spec.DeployGroup.Active {
		if err := ingMgr.ReconcileIngress(ctx, &gs, ordinals); err != nil {
			log.Error(err, "Failed to reconcile ingress")
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "IngressReconcileFailed", err.Error())
			r.setCondition(&gs, "Available", metav1.ConditionFalse, "IngressReconcileFailed", err.Error())
			_ = r.Status().Update(ctx, &gs)
			return ctrl.Result{}, err
		}
		r.setCondition(&gs, "Available", metav1.ConditionTrue, "AllIngressPathsReady",
			fmt.Sprintf("Ingress paths synced for %d connector pods", len(ordinals)))
		r.setCondition(&gs, "TrafficActive", metav1.ConditionTrue, "Active", "This deployment group is receiving traffic")
	} else {
		if err := ingMgr.DeleteIngress(ctx, &gs); err != nil {
			log.Error(err, "Failed to delete ingress for standby group")
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "IngressDeleteFailed", err.Error())
			r.setCondition(&gs, "Available", metav1.ConditionFalse, "IngressDeleteFailed", err.Error())
		}
		r.setCondition(&gs, "Available", metav1.ConditionTrue, "Standby",
			"Standby, no ingress active")
		r.setCondition(&gs, "TrafficActive", metav1.ConditionFalse, "Standby", "This deployment group is not receiving traffic")
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

	return ctrl.Result{}, nil
}

func (r *GameServiceReconciler) setCondition(gs *zzrrv1alpha1.GameService, condType string, status metav1.ConditionStatus, reason, message string) {
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
		LastTransitionTime: metav1.Now(),
	})
}

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
