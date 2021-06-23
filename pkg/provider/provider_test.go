package provider

import (
	"fmt"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/golang/mock/gomock"
	kedav1alpha1 "github.com/kedacore/keda/v2/api/v1alpha1"
	"github.com/kedacore/keda/v2/pkg/mock/mock_client"
	mock_scalers "github.com/kedacore/keda/v2/pkg/mock/mock_scaler"
	"github.com/kedacore/keda/v2/pkg/mock/mock_scaling"
	"github.com/kubernetes-sigs/custom-metrics-apiserver/pkg/provider"
	"k8s.io/api/autoscaling/v2beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	"testing"
)

const metricName = "some_metric_name"

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

type GinkgoTestReporter struct{}

func (g GinkgoTestReporter) Errorf(format string, args ...interface{}) {
	Fail(fmt.Sprintf(format, args...))
}

func (g GinkgoTestReporter) Fatalf(format string, args ...interface{}) {
	Fail(fmt.Sprintf(format, args...))
}

var _ = Describe("provider", func() {
	var (
		scaleHandler      *mock_scaling.MockScaleHandler
		client            *mock_client.MockClient
		providerUnderTest *KedaProvider
		scaler            *mock_scalers.MockScaler
		ctrl              *gomock.Controller
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoTestReporter{})
		scaleHandler = mock_scaling.NewMockScaleHandler(ctrl)
		client = mock_client.NewMockClient(ctrl)
		providerUnderTest = &KedaProvider{
			values:           make(map[provider.CustomMetricInfo]int64),
			externalMetrics:  make([]externalMetric, 2, 10),
			client:           client,
			scaleHandler:     scaleHandler,
			watchedNamespace: "",
		}
		scaler = mock_scalers.NewMockScaler(ctrl)
	})

	It("should return the expected metric when fallback is disabled", func() {

		expectedMetricValue := int64(5)
		primeGetMetrics(scaler, expectedMetricValue)
		so := buildScaledObject(nil, nil)
		metricSpec := createMetricSpec(3)

		metrics, err := providerUnderTest.getMetricsWithFallback(scaler, metricName, nil, so, metricSpec)

		Expect(err).ToNot(HaveOccurred())
		value, _ := metrics[0].Value.AsInt64()
		Expect(value).Should(Equal(expectedMetricValue))
	})

	It("should reset the health status when scaler metrics are available", func() {
		expectedMetricValue := int64(6)
		startingNumberOfFailures := uint32(5)
		primeGetMetrics(scaler, expectedMetricValue)

		so := buildScaledObject(
			&kedav1alpha1.Fallback{
				FailureThreshold: uint32(3),
				Replicas:         uint32(10),
			},
			&kedav1alpha1.ScaledObjectStatus{
				Health: map[string]kedav1alpha1.HealthStatus{
					metricName: {
						NumberOfFailures: &startingNumberOfFailures,
						Status:           kedav1alpha1.HealthStatusFailing,
					},
				},
			},
		)

		metricSpec := createMetricSpec(3)
		statusWriter := mock_client.NewMockStatusWriter(ctrl)
		statusWriter.EXPECT().Update(gomock.Any(), gomock.Any())
		client.EXPECT().Status().Return(statusWriter)

		metrics, err := providerUnderTest.getMetricsWithFallback(scaler, metricName, nil, so, metricSpec)

		Expect(err).ToNot(HaveOccurred())
		value, _ := metrics[0].Value.AsInt64()
		Expect(value).Should(Equal(expectedMetricValue))
		Expect(*so.Status.Health[metricName].NumberOfFailures).Should(Equal(uint32(0)))
		Expect(so.Status.Health[metricName].Status).Should(Equal(kedav1alpha1.HealthStatusHappy))
	})
})

func buildScaledObject(fallbackConfig *kedav1alpha1.Fallback, status *kedav1alpha1.ScaledObjectStatus) *kedav1alpha1.ScaledObject {
	scaledObject := &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{Name: "clean-up-test", Namespace: "default"},
		Spec: kedav1alpha1.ScaledObjectSpec{
			ScaleTargetRef: &kedav1alpha1.ScaleTarget{
				Name: "myapp",
			},
			Triggers: []kedav1alpha1.ScaleTriggers{
				{
					Type: "cron",
					Metadata: map[string]string{
						"timezone":        "UTC",
						"start":           "0 * * * *",
						"end":             "1 * * * *",
						"desiredReplicas": "1",
					},
				},
			},
			Fallback: fallbackConfig,
		},
	}

	if status != nil {
		scaledObject.Status = *status
	}

	return scaledObject
}

func primeGetMetrics(scaler *mock_scalers.MockScaler, value int64) {
	expectedMetric := external_metrics.ExternalMetricValue{
		MetricName: metricName,
		Value:      *resource.NewQuantity(value, resource.DecimalSI),
		Timestamp:  metav1.Now(),
	}

	scaler.EXPECT().GetMetrics(gomock.Any(), gomock.Eq(metricName), gomock.Any()).Return([]external_metrics.ExternalMetricValue{expectedMetric}, nil)
}

func createMetricSpec(averageValue int) v2beta2.MetricSpec {
	qty := resource.NewQuantity(int64(averageValue), resource.DecimalSI)
	return v2beta2.MetricSpec{
		External: &v2beta2.ExternalMetricSource{
			Target: v2beta2.MetricTarget{
				Type:         v2beta2.AverageValueMetricType,
				AverageValue: qty,
			},
		},
	}
}
