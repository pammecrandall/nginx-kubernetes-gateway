package status_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/gateway-api/apis/v1beta1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/nginxinc/nginx-kubernetes-gateway/internal/helpers"
	"github.com/nginxinc/nginx-kubernetes-gateway/internal/state"
	"github.com/nginxinc/nginx-kubernetes-gateway/internal/status"
	"github.com/nginxinc/nginx-kubernetes-gateway/internal/status/statusfakes"
)

var _ = Describe("Updater", func() {
	const gcName = "my-class"

	var (
		updater         status.Updater
		client          client.Client
		fakeClockTime   metav1.Time
		gatewayCtrlName string
	)

	BeforeEach(OncePerOrdered, func() {
		scheme := runtime.NewScheme()

		Expect(gatewayv1beta1.AddToScheme(scheme)).Should(Succeed())

		client = fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		// Rfc3339Copy() removes the monotonic clock reading
		// We need to remove it, because updating the status in the FakeClient and then getting the resource back
		// involves encoding and decoding the resource to/from JSON, which removes the monotonic clock reading.
		fakeClockTime = metav1.NewTime(time.Now()).Rfc3339Copy()
		fakeClock := &statusfakes.FakeClock{}
		fakeClock.NowReturns(fakeClockTime)

		gatewayCtrlName = "test.example.com"

		updater = status.NewUpdater(status.UpdaterConfig{
			GatewayCtlrName:  gatewayCtrlName,
			GatewayClassName: gcName,
			Client:           client,
			Logger:           zap.New(),
			Clock:            fakeClock,
		})
	})

	Describe("Process status updates", Ordered, func() {
		var (
			gc            *v1beta1.GatewayClass
			gw, ignoredGw *v1beta1.Gateway
			hr            *v1beta1.HTTPRoute

			createStatuses = func(valid bool, generation int64) state.Statuses {
				var gcErrorMsg string
				if !valid {
					gcErrorMsg = "error"
				}

				return state.Statuses{
					GatewayClassStatus: &state.GatewayClassStatus{
						Valid:              valid,
						ErrorMsg:           gcErrorMsg,
						ObservedGeneration: generation,
					},
					GatewayStatus: &state.GatewayStatus{
						NsName: types.NamespacedName{Namespace: "test", Name: "gateway"},
						ListenerStatuses: map[string]state.ListenerStatus{
							"http": {
								Valid:          valid,
								AttachedRoutes: 1,
							},
						},
					},
					IgnoredGatewayStatuses: map[types.NamespacedName]state.IgnoredGatewayStatus{
						{Namespace: "test", Name: "ignored-gateway"}: {
							ObservedGeneration: generation,
						},
					},
					HTTPRouteStatuses: map[types.NamespacedName]state.HTTPRouteStatus{
						{Namespace: "test", Name: "route1"}: {
							ParentStatuses: map[string]state.ParentStatus{
								"http": {
									Attached: valid,
								},
							},
						},
					},
				}
			}

			createExpectedGc = func(status metav1.ConditionStatus, generation int64, reason string, msg string) *v1beta1.GatewayClass {
				return &v1beta1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: gcName,
					},
					TypeMeta: metav1.TypeMeta{
						Kind:       "GatewayClass",
						APIVersion: "gateway.networking.k8s.io/v1beta1",
					},
					Status: v1beta1.GatewayClassStatus{
						Conditions: []metav1.Condition{
							{
								Type:               string(v1beta1.GatewayClassConditionStatusAccepted),
								Status:             status,
								ObservedGeneration: generation,
								LastTransitionTime: fakeClockTime,
								Reason:             reason,
								Message:            msg,
							},
						},
					},
				}
			}

			createExpectedGw = func(status metav1.ConditionStatus, reason string) *v1beta1.Gateway {
				return &v1beta1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "test",
						Name:      "gateway",
					},
					TypeMeta: metav1.TypeMeta{
						Kind:       "Gateway",
						APIVersion: "gateway.networking.k8s.io/v1beta1",
					},
					Status: v1beta1.GatewayStatus{
						Listeners: []gatewayv1beta1.ListenerStatus{
							{
								Name: "http",
								SupportedKinds: []v1beta1.RouteGroupKind{
									{
										Kind: "HTTPRoute",
									},
								},
								AttachedRoutes: 1,
								Conditions: []metav1.Condition{
									{
										Type:               string(v1beta1.ListenerConditionReady),
										Status:             status,
										ObservedGeneration: 123,
										LastTransitionTime: fakeClockTime,
										Reason:             reason,
									},
								},
							},
						},
					},
				}
			}

			createExpectedIgnoredGw = func() *v1beta1.Gateway {
				return &v1beta1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "test",
						Name:      "ignored-gateway",
					},
					TypeMeta: metav1.TypeMeta{
						Kind:       "Gateway",
						APIVersion: "gateway.networking.k8s.io/v1beta1",
					},
					Status: v1beta1.GatewayStatus{
						Conditions: []metav1.Condition{
							{
								Type:               string(v1beta1.GatewayConditionReady),
								Status:             metav1.ConditionFalse,
								ObservedGeneration: 1,
								LastTransitionTime: fakeClockTime,
								Reason:             string(status.GetawayReasonGatewayConflict),
								Message:            status.GatewayMessageGatewayConflict,
							},
						},
					},
				}
			}

			createExpectedHR = func() *v1beta1.HTTPRoute {
				return &v1beta1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "test",
						Name:      "route1",
					},
					TypeMeta: metav1.TypeMeta{
						Kind:       "HTTPRoute",
						APIVersion: "gateway.networking.k8s.io/v1beta1",
					},
					Status: gatewayv1beta1.HTTPRouteStatus{
						RouteStatus: gatewayv1beta1.RouteStatus{
							Parents: []gatewayv1beta1.RouteParentStatus{
								{
									ControllerName: gatewayv1beta1.GatewayController(gatewayCtrlName),
									ParentRef: gatewayv1beta1.ParentReference{
										Namespace:   (*v1beta1.Namespace)(helpers.GetStringPointer("test")),
										Name:        "gateway",
										SectionName: (*v1beta1.SectionName)(helpers.GetStringPointer("http")),
									},
									Conditions: []metav1.Condition{
										{
											Type:               string(gatewayv1beta1.RouteConditionAccepted),
											Status:             metav1.ConditionTrue,
											ObservedGeneration: 123,
											LastTransitionTime: fakeClockTime,
											Reason:             "Accepted",
										},
									},
								},
							},
						},
					},
				}
			}
		)

		BeforeAll(func() {
			gc = &v1beta1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: gcName,
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "GatewayClass",
					APIVersion: "gateway.networking.k8s.io/v1beta1",
				},
			}
			gw = &v1beta1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test",
					Name:      "gateway",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Gateway",
					APIVersion: "gateway.networking.k8s.io/v1beta1",
				},
			}
			ignoredGw = &v1beta1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test",
					Name:      "ignored-gateway",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Gateway",
					APIVersion: "gateway.networking.k8s.io/v1beta1",
				},
			}
			hr = &v1beta1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test",
					Name:      "route1",
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "HTTPRoute",
					APIVersion: "gateway.networking.k8s.io/v1beta1",
				},
			}
		})

		It("should create resources in the API server", func() {
			Expect(client.Create(context.Background(), gc)).Should(Succeed())
			Expect(client.Create(context.Background(), gw)).Should(Succeed())
			Expect(client.Create(context.Background(), ignoredGw)).Should(Succeed())
			Expect(client.Create(context.Background(), hr)).Should(Succeed())
		})

		It("should update statuses", func() {
			updater.Update(context.Background(), createStatuses(true, 1))
		})

		It("should have the updated status of GatewayClass in the API server", func() {
			latestGc := &v1beta1.GatewayClass{}
			expectedGc := createExpectedGc(metav1.ConditionTrue, 1, string(v1beta1.GatewayClassConditionStatusAccepted), "GatewayClass has been accepted")

			err := client.Get(context.Background(), types.NamespacedName{Name: gcName}, latestGc)
			Expect(err).Should(Not(HaveOccurred()))

			expectedGc.ResourceVersion = latestGc.ResourceVersion // updating the status changes the ResourceVersion

			Expect(helpers.Diff(expectedGc, latestGc)).To(BeEmpty())
		})

		It("should have the updated status of Gateway in the API server", func() {
			latestGw := &v1beta1.Gateway{}
			expectedGw := createExpectedGw(metav1.ConditionTrue, string(v1beta1.ListenerReasonReady))

			err := client.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: "gateway"}, latestGw)
			Expect(err).Should(Not(HaveOccurred()))

			expectedGw.ResourceVersion = latestGw.ResourceVersion

			Expect(helpers.Diff(expectedGw, latestGw)).To(BeEmpty())
		})

		It("should have the updated status of ignored Gateway in the API server", func() {
			latestGw := &v1beta1.Gateway{}
			expectedGw := createExpectedIgnoredGw()

			err := client.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: "ignored-gateway"}, latestGw)
			Expect(err).Should(Not(HaveOccurred()))

			expectedGw.ResourceVersion = latestGw.ResourceVersion

			Expect(helpers.Diff(expectedGw, latestGw)).To(BeEmpty())
		})

		It("should have the updated status of HTTPRoute in the API server", func() {
			latestHR := &v1beta1.HTTPRoute{}
			expectedHR := createExpectedHR()

			err := client.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: "route1"}, latestHR)
			Expect(err).Should(Not(HaveOccurred()))

			expectedHR.ResourceVersion = latestHR.ResourceVersion

			Expect(helpers.Diff(expectedHR, latestHR)).To(BeEmpty())
		})

		It("should update statuses with canceled context - function normally returns", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			updater.Update(ctx, createStatuses(false, 2))
		})

		When("updating with canceled context", func() {
			It("should have the updated status of GatewayClass in the API server", func() {
				latestGc := &v1beta1.GatewayClass{}
				expectedGc := createExpectedGc(metav1.ConditionFalse, 2, string(v1beta1.GatewayClassConditionStatusAccepted), "GatewayClass has been rejected: error")

				err := client.Get(context.Background(), types.NamespacedName{Name: gcName}, latestGc)
				Expect(err).Should(Not(HaveOccurred()))

				expectedGc.ResourceVersion = latestGc.ResourceVersion

				Expect(helpers.Diff(expectedGc, latestGc)).To(BeEmpty())
			})

			It("should have the updated status of Gateway in the API server", func() {
				latestGw := &v1beta1.Gateway{}
				expectedGw := createExpectedGw(metav1.ConditionFalse, string(v1beta1.ListenerReasonInvalid))

				err := client.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: "gateway"}, latestGw)
				Expect(err).Should(Not(HaveOccurred()))

				expectedGw.ResourceVersion = latestGw.ResourceVersion

				Expect(helpers.Diff(expectedGw, latestGw)).To(BeEmpty())
			})

			It("should not have the updated status of ignored Gateway in the API server", func() {
				latestGw := &v1beta1.Gateway{}
				expectedGw := createExpectedIgnoredGw()

				err := client.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: "ignored-gateway"}, latestGw)
				Expect(err).Should(Not(HaveOccurred()))

				expectedGw.ResourceVersion = latestGw.ResourceVersion

				// if the status was updated, we would see a different ObservedGeneration
				Expect(helpers.Diff(expectedGw, latestGw)).To(BeEmpty())
			})

			It("should not have the updated status of HTTPRoute in the API server", func() {
				latestHR := &v1beta1.HTTPRoute{}
				expectedHR := createExpectedHR()

				err := client.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: "route1"}, latestHR)
				Expect(err).Should(Not(HaveOccurred()))

				expectedHR.ResourceVersion = latestHR.ResourceVersion

				// if the status was updated, we would see the route rejected (Accepted = false)
				Expect(helpers.Diff(expectedHR, latestHR)).To(BeEmpty())
			})
		})
	})
})
