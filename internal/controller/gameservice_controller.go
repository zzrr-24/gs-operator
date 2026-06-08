package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
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

// +kubebuilder:rbac:groups=gs.zzrr.io,resources=gameservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gs.zzrr.io,resources=gameservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gs.zzrr.io,resources=gameservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

const gameServiceFinalizer = "gs.zzrr.io/finalizer"

type GameServiceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// nolint:gocyclo
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

	if err := validateRouteConfig(gs.Spec.Route); err != nil {
		log.Error(err, "Invalid route config")
		r.Recorder.Event(&gs, corev1.EventTypeWarning, "InvalidRouteConfig", err.Error())
		return ctrl.Result{}, err
	}

	svcMgr := NewConnectorServiceManager(r.Client, r.Scheme)
	ingMgr := NewIngressManager(r.Client, r.Scheme)
	routeMgr := NewHTTPRouteManager(r.Client, r.Scheme)

	pods, err := svcMgr.ListConnectorPods(ctx,
		gs.Spec.ConnectorNamespace,
		gs.Spec.PodLabelKey,
		gs.Spec.PodLabelValue,
	)
	if err != nil {
		log.Error(err, "Failed to list connector pods")
		r.Recorder.Event(&gs, corev1.EventTypeWarning, "PodListFailed", err.Error())
		return ctrl.Result{}, err
	}

	servingPods := filterServiceableConnectorPods(pods)

	var podNames []string
	for _, pod := range servingPods {
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

		for i := range servingPods {
			pod := servingPods[i]
			eg.Go(func() error {
				if err := sem.Acquire(egCtx, 1); err != nil {
					return err
				}
				defer sem.Release(1)
				if _, err := svcMgr.EnsureService(egCtx, &pod, gs.Spec.Route.Port); err != nil {
					log.Error(err, "Failed to ensure service for pod", "pod", pod.Name)
					r.Recorder.Event(&gs, corev1.EventTypeWarning, "ServiceCreateFailed", err.Error())
					return err
				}
				return nil
			})
		}

		if err := eg.Wait(); err != nil {
			log.Error(err, "Failed to ensure services, skipping remaining reconciliation")
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "ServiceEnsureFailed",
				"Some services could not be ensured, will retry")
			return ctrl.Result{}, err
		}
	}

	if err := svcMgr.DeleteOrphanServices(ctx, gs.Spec.ConnectorNamespace, activeOrdinals); err != nil {
		log.Error(err, "Failed to delete orphan services")
		r.Recorder.Event(&gs, corev1.EventTypeWarning, "OrphanCleanupFailed", err.Error())
	}

	if gs.Spec.DeployGroup.Active {
		switch effectiveTrafficMode(&gs) {
		case zzrrv1alpha1.TrafficModeGateway:
			if err := ingMgr.DeleteIngress(ctx, &gs); err != nil {
				log.Error(err, "Failed to delete stale ingress for gateway mode")
				r.Recorder.Event(&gs, corev1.EventTypeWarning, "StaleIngressDeleteFailed", err.Error())
				r.setCondition(&gs, "Available", metav1.ConditionFalse, "StaleIngressDeleteFailed", err.Error())
				_ = r.Status().Update(ctx, &gs)
				return ctrl.Result{}, err
			}
			if err := routeMgr.ReconcileHTTPRoute(ctx, &gs, ordinals); err != nil {
				log.Error(err, "Failed to reconcile HTTPRoute")
				r.Recorder.Event(&gs, corev1.EventTypeWarning, "HTTPRouteReconcileFailed", err.Error())
				r.setCondition(&gs, "Available", metav1.ConditionFalse, "HTTPRouteReconcileFailed", err.Error())
				_ = r.Status().Update(ctx, &gs)
				return ctrl.Result{}, err
			}
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
			r.setCondition(&gs, "Available", metav1.ConditionTrue, "AllHTTPRoutePathsReady",
				fmt.Sprintf("HTTPRoute paths synced for %d connector pods", len(ordinals)))
		default:
			if err := routeMgr.DeleteHTTPRoute(ctx, &gs); err != nil {
				log.Error(err, "Failed to delete stale HTTPRoute for ingress mode")
				r.Recorder.Event(&gs, corev1.EventTypeWarning, "StaleHTTPRouteDeleteFailed", err.Error())
				r.setCondition(&gs, "Available", metav1.ConditionFalse, "StaleHTTPRouteDeleteFailed", err.Error())
				_ = r.Status().Update(ctx, &gs)
				return ctrl.Result{}, err
			}
			if err := ingMgr.ReconcileIngress(ctx, &gs, ordinals); err != nil {
				log.Error(err, "Failed to reconcile ingress")
				r.Recorder.Event(&gs, corev1.EventTypeWarning, "IngressReconcileFailed", err.Error())
				r.setCondition(&gs, "Available", metav1.ConditionFalse, "IngressReconcileFailed", err.Error())
				_ = r.Status().Update(ctx, &gs)
				return ctrl.Result{}, err
			}
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
			r.setCondition(&gs, "Available", metav1.ConditionTrue, "AllIngressPathsReady",
				fmt.Sprintf("Ingress paths synced for %d connector pods", len(ordinals)))
		}
		r.setCondition(&gs, "TrafficActive", metav1.ConditionTrue, "Active", "This deployment group is receiving traffic")
	} else {
		if err := ingMgr.DeleteIngress(ctx, &gs); err != nil {
			log.Error(err, "Failed to delete ingress for standby group")
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "IngressDeleteFailed", err.Error())
			r.setCondition(&gs, "Available", metav1.ConditionFalse, "IngressDeleteFailed", err.Error())
		}
		if err := routeMgr.DeleteHTTPRoute(ctx, &gs); err != nil {
			log.Error(err, "Failed to delete HTTPRoute for standby group")
			r.Recorder.Event(&gs, corev1.EventTypeWarning, "HTTPRouteDeleteFailed", err.Error())
			r.setCondition(&gs, "Available", metav1.ConditionFalse, "HTTPRouteDeleteFailed", err.Error())
		}
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
		r.setCondition(&gs, "Available", metav1.ConditionTrue, "Standby",
			"Standby, no traffic entry active")
		r.setCondition(&gs, "TrafficActive", metav1.ConditionFalse, "Standby", "This deployment group is not receiving traffic")
	}

	var connectorImage string
	for i := range servingPods {
		if servingPods[i].Status.Phase == corev1.PodRunning && len(servingPods[i].Spec.Containers) > 0 {
			connectorImage = servingPods[i].Spec.Containers[0].Image
			break
		}
	}
	if connectorImage == "" {
		for i := range servingPods {
			if servingPods[i].Status.Phase == corev1.PodPending && len(servingPods[i].Spec.Containers) > 0 {
				connectorImage = servingPods[i].Spec.Containers[0].Image
				break
			}
		}
	}

	var latestGS zzrrv1alpha1.GameService
	if err := r.Get(ctx, req.NamespacedName, &latestGS); err != nil {
		log.Error(err, "Failed to re-fetch GameService before status update")
		return ctrl.Result{}, err
	}

	latestGS.Status.ConnectorImage = connectorImage
	latestGS.Status.ConnectorCount = int32(len(ordinals))
	latestGS.Status.ObservedGeneration = latestGS.Generation
	latestGS.Status.Conditions = gs.Status.Conditions

	if err := r.Status().Update(ctx, &latestGS); err != nil {
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
		retentionStart := r.getInactiveSince(log, &gs)
		if retentionStart.Add(duration).Before(time.Now()) {
			log.Info("Retention period expired, deleting GameService", "name", gs.Name)
			if err := r.Delete(ctx, &gs); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		requeueAfter := time.Until(retentionStart.Add(duration))
		log.Info("Retention period active, will auto-delete", "requeueAfter", requeueAfter)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	return ctrl.Result{}, nil
}

func validateRouteConfig(route zzrrv1alpha1.RouteConfig) error {
	if route.Host == "" || route.PathType == "" || route.PathPrefix == "" || route.Port <= 0 {
		return fmt.Errorf("spec.route is incomplete, migrate host, pathType, pathPrefix, and port from spec.ingress")
	}
	return nil
}

func filterServiceableConnectorPods(pods []corev1.Pod) []corev1.Pod {
	servingPods := make([]corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}
		servingPods = append(servingPods, pod)
	}
	return servingPods
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

func (r *GameServiceReconciler) getInactiveSince(log logr.Logger, gs *zzrrv1alpha1.GameService) time.Time {
	for _, c := range gs.Status.Conditions {
		if c.Type == "TrafficActive" && c.Status == metav1.ConditionFalse {
			return c.LastTransitionTime.Time
		}
	}
	log.Info("No TrafficActive=False condition found, starting retention timer from now",
		"name", gs.Name, "namespace", gs.Namespace)
	return time.Now()
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
	routeMgr := NewHTTPRouteManager(r.Client, r.Scheme)
	if err := routeMgr.DeleteHTTPRoute(ctx, gs); err != nil {
		log.Error(err, "Failed to delete HTTPRoute during finalization")
		return ctrl.Result{}, err
	}
	if err := ingMgr.DeleteExtraIngress(ctx, gs); err != nil {
		log.Error(err, "Failed to delete extra Ingress during finalization")
		return ctrl.Result{}, err
	}
	if err := routeMgr.DeleteExtraHTTPRoute(ctx, gs); err != nil {
		log.Error(err, "Failed to delete extra HTTPRoute during finalization")
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

func effectiveTrafficMode(gs *zzrrv1alpha1.GameService) zzrrv1alpha1.TrafficMode {
	if gs.Spec.TrafficMode == "" {
		return zzrrv1alpha1.TrafficModeIngress
	}
	return gs.Spec.TrafficMode
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

	log := log.FromContext(ctx)

	var list zzrrv1alpha1.GameServiceList
	if err := r.List(ctx, &list, client.MatchingFields{"spec.connectorNamespace": pod.Namespace}); err != nil {
		log.Error(err, "Failed to map pod to GameService", "pod", pod.Name)
		return nil
	}

	var requests []reconcile.Request
	for _, gs := range list.Items {
		if pod.Labels[gs.Spec.PodLabelKey] == gs.Spec.PodLabelValue {
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
