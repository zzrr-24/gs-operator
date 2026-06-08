package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

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
		deleteIfExists(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "connector-0", Namespace: testNs}})
		deleteIfExists(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "connector-0-svc", Namespace: testNs}})
		deleteIfExists(ctx, &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "game-ingress-blue", Namespace: testNs}})
		deleteIfExists(ctx, &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "game-route-blue", Namespace: testNs}})
		deleteIfExists(ctx, &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-ingress-blue", Namespace: testNs}})
		deleteIfExists(ctx, &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-ingress-green", Namespace: testNs}})
		deleteIfExists(ctx, &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-gateway-blue", Namespace: testNs}})
		deleteIfExists(ctx, &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-gateway-green", Namespace: testNs}})

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
				Route: zzrrv1alpha1.RouteConfig{
					Host:       "test.example.com",
					PathType:   "Prefix",
					PathPrefix: "/connector",
					Port:       80,
				},
				Ingress: &zzrrv1alpha1.IngressConfig{
					IngressClassName: "nginx",
				},
				PodLabelKey:        "app",
				PodLabelValue:      "connector",
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

	createGatewayGS := func(active bool) *zzrrv1alpha1.GameService {
		gs := createGS(active)
		gs.Spec.TrafficMode = zzrrv1alpha1.TrafficModeGateway
		gs.Spec.Ingress = nil
		gs.Spec.Gateway = &zzrrv1alpha1.GatewayConfig{
			ParentRef: zzrrv1alpha1.GatewayParentRef{
				Name:        "shared-gateway",
				Namespace:   "gateway-system",
				SectionName: "http",
			},
		}
		Expect(k8sClient.Update(ctx, gs)).To(Succeed())
		return gs
	}

	createConnectorPod := func(name string) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNs,
				Labels: map[string]string{
					"app":                                "connector",
					"statefulset.kubernetes.io/pod-name": name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "connector", Image: "connector:v1"},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
			},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	}

	addExtraIngress := func(gs *zzrrv1alpha1.GameService) {
		gs.Spec.Ingress.Annotations = map[string]string{
			"main-only": "true",
		}
		gs.Spec.Ingress.TLS = &zzrrv1alpha1.TLSConfig{
			SecretName: "game-tls",
		}
		gs.Spec.ExtraIngress = &zzrrv1alpha1.ExtraIngressConfig{
			Name: "gamelogin",
			Annotations: map[string]string{
				"extra-only": "true",
			},
			Paths: []zzrrv1alpha1.ExtraIngressPath{
				{
					PathType:    "Prefix",
					Path:        "/serverlogin",
					ServiceName: "logingame-blue",
					Port:        9020,
				},
			},
		}
		Expect(k8sClient.Update(ctx, gs)).To(Succeed())
	}

	addGatewayExtraIngress := func(gs *zzrrv1alpha1.GameService) {
		gs.Spec.ExtraIngress = &zzrrv1alpha1.ExtraIngressConfig{
			Name: "gamelogin",
			Annotations: map[string]string{
				"extra-route": "true",
			},
			Paths: []zzrrv1alpha1.ExtraIngressPath{
				{
					PathType:    "Prefix",
					Path:        "/serverlogin",
					ServiceName: "logingame-blue",
					Port:        9020,
				},
			},
		}
		Expect(k8sClient.Update(ctx, gs)).To(Succeed())
	}

	newStaleIngress := func(name string) *networkingv1.Ingress {
		pathType := networkingv1.PathTypePrefix
		return &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{
					{
						Host: "stale.example.com",
						IngressRuleValue: networkingv1.IngressRuleValue{
							HTTP: &networkingv1.HTTPIngressRuleValue{
								Paths: []networkingv1.HTTPIngressPath{
									{
										Path:     "/stale",
										PathType: &pathType,
										Backend: networkingv1.IngressBackend{
											Service: &networkingv1.IngressServiceBackend{
												Name: "stale-svc",
												Port: networkingv1.ServiceBackendPort{Number: 80},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}
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

	Context("When trafficMode is omitted", func() {
		It("should keep creating Ingress resources", func() {
			createGS(true)
			createConnectorPod("connector-0")
			reconcileOnce()
			reconcileOnce()

			ing := &networkingv1.Ingress{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "game-ingress-blue", Namespace: testNs}, ing)).To(Succeed())
			Expect(ing.Spec.IngressClassName).NotTo(BeNil())
			Expect(*ing.Spec.IngressClassName).To(Equal("nginx"))
			Expect(ing.Spec.Rules).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].HTTP.Paths).To(HaveLen(1))
			Expect(ing.Spec.Rules[0].HTTP.Paths[0].Path).To(Equal("/connector0"))
		})

		Context("When extra ingress is configured for Ingress traffic", func() {
			It("should create an extra Ingress for active groups", func() {
				gs := createGS(true)
				addExtraIngress(gs)
				createConnectorPod("connector-0")
				reconcileOnce()
				reconcileOnce()

				ing := &networkingv1.Ingress{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: testNs}, ing)).To(Succeed())
				Expect(ing.Spec.IngressClassName).NotTo(BeNil())
				Expect(*ing.Spec.IngressClassName).To(Equal("nginx"))
				Expect(ing.Spec.Rules).To(HaveLen(1))
				Expect(ing.Spec.Rules[0].Host).To(Equal("test.example.com"))
				Expect(ing.Spec.Rules[0].HTTP.Paths).To(HaveLen(1))
				Expect(ing.Spec.Rules[0].HTTP.Paths[0].Path).To(Equal("/serverlogin"))
				Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name).To(Equal("logingame-blue"))
				Expect(ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number).To(Equal(int32(9020)))
				Expect(ing.Annotations).To(HaveKeyWithValue("extra-only", "true"))
				Expect(ing.Annotations).NotTo(HaveKey("main-only"))
				Expect(ing.Spec.TLS).To(HaveLen(1))
				Expect(ing.Spec.TLS[0].Hosts).To(Equal([]string{"test.example.com"}))
				Expect(ing.Spec.TLS[0].SecretName).To(Equal("game-tls"))
			})

			It("should delete stale extra HTTPRoute when active traffic mode is Ingress", func() {
				gs := createGS(true)
				addExtraIngress(gs)
				staleRoute := &gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gamelogin-gateway-blue",
						Namespace: testNs,
					},
				}
				Expect(k8sClient.Create(ctx, staleRoute)).To(Succeed())
				createConnectorPod("connector-0")
				reconcileOnce()
				reconcileOnce()

				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: testNs}, staleRoute))).To(BeTrue())
			})
		})
	})

	Context("When trafficMode is Gateway", func() {
		It("should create an HTTPRoute for active groups", func() {
			createGatewayGS(true)
			createConnectorPod("connector-0")
			reconcileOnce()
			reconcileOnce()

			route := &gatewayv1.HTTPRoute{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "game-route-blue", Namespace: testNs}, route)).To(Succeed())
			Expect(route.Annotations).To(BeNil())
			Expect(route.Spec.ParentRefs).To(HaveLen(1))
			Expect(string(route.Spec.ParentRefs[0].Name)).To(Equal("shared-gateway"))
			Expect(route.Spec.ParentRefs[0].Namespace).NotTo(BeNil())
			Expect(string(*route.Spec.ParentRefs[0].Namespace)).To(Equal("gateway-system"))
			Expect(route.Spec.ParentRefs[0].SectionName).NotTo(BeNil())
			Expect(string(*route.Spec.ParentRefs[0].SectionName)).To(Equal("http"))
			Expect(route.Spec.Hostnames).To(Equal([]gatewayv1.Hostname{"test.example.com"}))
			Expect(route.Spec.Rules).To(HaveLen(1))
			Expect(route.Spec.Rules[0].Matches).To(HaveLen(1))
			Expect(*route.Spec.Rules[0].Matches[0].Path.Type).To(Equal(gatewayv1.PathMatchPathPrefix))
			Expect(*route.Spec.Rules[0].Matches[0].Path.Value).To(Equal("/connector0"))
			Expect(route.Spec.Rules[0].BackendRefs).To(HaveLen(1))
			Expect(string(route.Spec.Rules[0].BackendRefs[0].Name)).To(Equal("connector-0-svc"))
			Expect(*route.Spec.Rules[0].BackendRefs[0].Port).To(Equal(gatewayv1.PortNumber(80)))
		})

		It("should create an extra HTTPRoute for active groups", func() {
			gs := createGatewayGS(true)
			addGatewayExtraIngress(gs)
			createConnectorPod("connector-0")
			reconcileOnce()
			reconcileOnce()

			route := &gatewayv1.HTTPRoute{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: testNs}, route)).To(Succeed())
			Expect(route.Annotations).To(HaveKeyWithValue("extra-route", "true"))
			Expect(route.Spec.ParentRefs).To(HaveLen(1))
			Expect(string(route.Spec.ParentRefs[0].Name)).To(Equal("shared-gateway"))
			Expect(route.Spec.ParentRefs[0].Namespace).NotTo(BeNil())
			Expect(string(*route.Spec.ParentRefs[0].Namespace)).To(Equal("gateway-system"))
			Expect(route.Spec.ParentRefs[0].SectionName).NotTo(BeNil())
			Expect(string(*route.Spec.ParentRefs[0].SectionName)).To(Equal("http"))
			Expect(route.Spec.Hostnames).To(Equal([]gatewayv1.Hostname{"test.example.com"}))
			Expect(route.Spec.Rules).To(HaveLen(1))
			Expect(route.Spec.Rules[0].Matches).To(HaveLen(1))
			Expect(*route.Spec.Rules[0].Matches[0].Path.Type).To(Equal(gatewayv1.PathMatchPathPrefix))
			Expect(*route.Spec.Rules[0].Matches[0].Path.Value).To(Equal("/serverlogin"))
			Expect(route.Spec.Rules[0].BackendRefs).To(HaveLen(1))
			Expect(string(route.Spec.Rules[0].BackendRefs[0].Name)).To(Equal("logingame-blue"))
			Expect(*route.Spec.Rules[0].BackendRefs[0].Port).To(Equal(gatewayv1.PortNumber(9020)))
		})

		It("should delete stale extra Ingress when active traffic mode is Gateway", func() {
			gs := createGatewayGS(true)
			addGatewayExtraIngress(gs)
			staleIngress := newStaleIngress("gamelogin-ingress-blue")
			Expect(k8sClient.Create(ctx, staleIngress)).To(Succeed())
			createConnectorPod("connector-0")
			reconcileOnce()
			reconcileOnce()

			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: testNs}, staleIngress))).To(BeTrue())
		})

		It("should delete HTTPRoute resources for standby groups", func() {
			createGatewayGS(true)
			createConnectorPod("connector-0")
			reconcileOnce()
			reconcileOnce()

			route := &gatewayv1.HTTPRoute{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "game-route-blue", Namespace: testNs}, route)).To(Succeed())

			var updated zzrrv1alpha1.GameService
			Expect(k8sClient.Get(ctx, typeNamespacedName, &updated)).To(Succeed())
			updated.Spec.DeployGroup.Active = false
			Expect(k8sClient.Update(ctx, &updated)).To(Succeed())
			reconcileOnce()

			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "game-route-blue", Namespace: testNs}, route))).To(BeTrue())
		})
	})

	Context("When Gateway API CRDs are not installed", func() {
		It("should ignore HTTPRoute cleanup for Ingress compatibility", func() {
			interceptedClient := fake.NewClientBuilder().
				WithScheme(k8sClient.Scheme()).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(
						context.Context,
						client.WithWatch,
						client.ObjectKey,
						client.Object,
						...client.GetOption,
					) error {
						return &apimeta.NoKindMatchError{
							GroupKind: schema.GroupKind{
								Group: gatewayv1.GroupVersion.Group,
								Kind:  "HTTPRoute",
							},
							SearchedVersions: []string{gatewayv1.GroupVersion.Version},
						}
					},
				}).
				Build()
			mgr := NewHTTPRouteManager(interceptedClient, k8sClient.Scheme())

			Expect(mgr.DeleteHTTPRoute(ctx, &zzrrv1alpha1.GameService{
				Spec: zzrrv1alpha1.GameServiceSpec{
					ConnectorNamespace: testNs,
					DeployGroup: zzrrv1alpha1.DeployGroupConfig{
						Role: "blue",
					},
				},
			})).To(Succeed())
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

		It("should delete extra traffic resources for the standby role only", func() {
			gs := createGS(false)
			addExtraIngress(gs)
			blueIngress := newStaleIngress("gamelogin-ingress-blue")
			blueRoute := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "gamelogin-gateway-blue", Namespace: testNs},
			}
			greenIngress := newStaleIngress("gamelogin-ingress-green")
			Expect(k8sClient.Create(ctx, blueIngress)).To(Succeed())
			Expect(k8sClient.Create(ctx, blueRoute)).To(Succeed())
			Expect(k8sClient.Create(ctx, greenIngress)).To(Succeed())

			reconcileOnce()
			reconcileOnce()

			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-blue", Namespace: testNs}, blueIngress))).To(BeTrue())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-gateway-blue", Namespace: testNs}, blueRoute))).To(BeTrue())
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "gamelogin-ingress-green", Namespace: testNs}, greenIngress)).To(Succeed())
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

	Context("ValidateRouteConfig", func() {
		It("should reject legacy objects without spec.route", func() {
			Expect(validateRouteConfig(zzrrv1alpha1.RouteConfig{})).To(MatchError(
				"spec.route is incomplete, migrate host, pathType, pathPrefix, and port from spec.ingress",
			))
		})

		It("should accept complete route config", func() {
			Expect(validateRouteConfig(zzrrv1alpha1.RouteConfig{
				Host:       "test.example.com",
				PathType:   "Prefix",
				PathPrefix: "/connector",
				Port:       80,
			})).To(Succeed())
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

func deleteIfExists(ctx context.Context, obj client.Object) {
	err := k8sClient.Delete(ctx, obj)
	if err != nil && !apierrors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}
