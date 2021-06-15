package provider

import (
	"github.com/golang/mock/gomock"
	kedav1alpha1 "github.com/kedacore/keda/v2/api/v1alpha1"
	"github.com/kedacore/keda/v2/pkg/mock/mock_client"
	mock_scalers "github.com/kedacore/keda/v2/pkg/mock/mock_scaler"
	"github.com/kedacore/keda/v2/pkg/mock/mock_scaling"
	"github.com/kubernetes-sigs/custom-metrics-apiserver/pkg/provider"
	"github.com/stretchr/testify/assert"
	"k8s.io/api/autoscaling/v2beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"testing"
)

const metricName = "some_metric_name"

func TestHappyPathFallbackDisabled(t *testing.T) {
	ctrl := gomock.NewController(t)
	scaleHandler := mock_scaling.NewMockScaleHandler(ctrl)
	client := mock_client.NewMockClient(ctrl)
	provider := &KedaProvider{
		values:           make(map[provider.CustomMetricInfo]int64),
		externalMetrics:  make([]externalMetric, 2, 10),
		client:           client,
		scaleHandler:     scaleHandler,
		watchedNamespace: "",
	}
	scaler := mock_scalers.NewMockScaler(ctrl)

	expectedMetricValue := int64(5)
	primeGetMetrics(scaler, expectedMetricValue)

	so := &kedav1alpha1.ScaledObject{
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
		},
	}

	metricSpec := createMetricSpec(3)

	metrics, err := provider.getMetricsWithFallback(scaler, metricName, nil, so, metricSpec)
	if err != nil {
		t.Fail()
	}

	value, _ := metrics[0].Value.AsInt64()

	assert.Equal(t, value, expectedMetricValue)
}

func TestFallbackIsResetWhenScalerMetricsAreAvailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	scaleHandler := mock_scaling.NewMockScaleHandler(ctrl)
	client := mock_client.NewMockClient(ctrl)
	provider := &KedaProvider{
		values:           make(map[provider.CustomMetricInfo]int64),
		externalMetrics:  make([]externalMetric, 2, 10),
		client:           client,
		scaleHandler:     scaleHandler,
		watchedNamespace: "",
	}
	scaler := mock_scalers.NewMockScaler(ctrl)

	expectedMetricValue := int64(6)
	primeGetMetrics(scaler, expectedMetricValue)

	so := &kedav1alpha1.ScaledObject{
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
			Fallback: &kedav1alpha1.Fallback{
				FailureThreshold: uint32(3),
				FallbackReplicas: uint32(10),
			},
		},
	}

	metricSpec := createMetricSpec(3)

	statusWriter := mock_client.NewMockStatusWriter(ctrl)
	statusWriter.EXPECT().Update(gomock.Any(), gomock.Any())
	client.EXPECT().Status().Return(statusWriter)

	metrics, err := provider.getMetricsWithFallback(scaler, metricName, nil, so, metricSpec)
	if err != nil {
		t.Fail()
	}

	value, _ := metrics[0].Value.AsInt64()

	assert.Equal(t, value, expectedMetricValue)
	assert.Equal(t, *so.Status.Fallback[metricName].NumberOfFailures, uint32(0))
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
