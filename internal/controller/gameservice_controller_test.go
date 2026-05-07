package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	zzrrv1alpha1 "gs-operator/api/v1alpha1"
)

var _ = Describe("GameService Controller", func() {
	ctx := context.Background()

	var testNs string
	var typeNamespacedName types.NamespacedName

	BeforeEach(func() {
		testNs = "default"
		typeNamespacedName = types.NamespacedName{
			Name:      "test-gs",
			Namespace: testNs,
		}
	})

	AfterEach(func() {
		var gs zzrrv1alpha1.GameService
		err := k8sClient.Get(ctx, typeNamespacedName, &gs)
		if err != nil {
			return
		}
		controllerutil.RemoveFinalizer(&gs, gameServiceFinalizer)
		Expect(k8sClient.Update(ctx, &gs)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &gs)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, typeNamespacedName, &gs))
		}, time.Second*10, time.Millisecond*100).Should(BeTrue())
	})

	reconciler := func() *GameServiceReconciler {
		return &GameServiceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
		}
	}

	createGS := func(active bool) *zzrrv1alpha1.GameService {
		gs := &zzrrv1alpha1.GameService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      typeNamespacedName.Name,
				Namespace: typeNamespacedName.Namespace,
			},
			Spec: zzrrv1alpha1.GameServiceSpec{
				Ingress: zzrrv1alpha1.IngressConfig{
					Host:             "test.example.com",
					IngressClassName: "nginx",
					PathType:         "Prefix",
					PathPrefix:       "/connector",
					Port:             80,
				},
				ConnectorNamespace: testNs,
				DeployGroup: zzrrv1alpha1.DeployGroupConfig{
					Role:   "blue",
					Active: active,
				},
			},
		}
		Expect(k8sClient.Create(ctx, gs)).To(Succeed())
		return gs
	}

	reconcileOnce := func() {
		_, err := reconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: typeNamespacedName,
		})
		Expect(err).NotTo(HaveOccurred())
	}

	Context("When CR is first created", func() {
		It("should add finalizer on first reconcile", func() {
			gs := createGS(true)
			reconcileOnce()
			reconcileOnce()

			updated := &zzrrv1alpha1.GameService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gs.Name, Namespace: gs.Namespace}, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement(gameServiceFinalizer))
		})
	})

	Context("When no connector pods exist", func() {
		It("should set conditions and connectorCount=0", func() {
			gs := createGS(true)
			reconcileOnce()
			reconcileOnce()

			updated := &zzrrv1alpha1.GameService{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gs.Name, Namespace: gs.Namespace}, updated)).To(Succeed())

			Expect(updated.Finalizers).To(ContainElement(gameServiceFinalizer))
			Expect(updated.Status.ConnectorCount).To(Equal(int32(0)))
			Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
		})
	})

	Context("When switching from active to inactive", func() {
		It("should set TrafficActive=False", func() {
			gs := createGS(true)
			reconcileOnce()
			reconcileOnce()

			var updated zzrrv1alpha1.GameService
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gs.Name, Namespace: gs.Namespace}, &updated)).To(Succeed())
			Expect(hasCondition(updated.Status.Conditions, "Available", metav1.ConditionTrue)).To(BeTrue())

			updated.Spec.DeployGroup.Active = false
			Expect(k8sClient.Update(ctx, &updated)).To(Succeed())
			reconcileOnce()

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gs.Name, Namespace: gs.Namespace}, &updated)).To(Succeed())
			Expect(hasCondition(updated.Status.Conditions, "TrafficActive", metav1.ConditionFalse)).To(BeTrue())
		})
	})

	Context("GetPodOrdinal", func() {
		It("should extract ordinal from pod name", func() {
			Expect(GetPodOrdinal("connector-0")).To(Equal("0"))
			Expect(GetPodOrdinal("connector-123")).To(Equal("123"))
			Expect(GetPodOrdinal("my-connector-abc-5")).To(Equal("5"))
			Expect(GetPodOrdinal("nohyphen")).To(Equal(""))
		})
	})

	Context("BuildConnectorOrdinals", func() {
		It("should deduplicate and sort ordinals", func() {
			mgr := &IngressManager{}
			result := mgr.BuildConnectorOrdinals([]string{"pod-3", "pod-1", "pod-2", "pod-1"})
			Expect(result).To(Equal([]string{"1", "2", "3"}))
		})
	})
})

func hasCondition(conditions []metav1.Condition, condType string, status metav1.ConditionStatus) bool {
	for _, c := range conditions {
		if c.Type == condType && c.Status == status {
			return true
		}
	}
	return false
}
