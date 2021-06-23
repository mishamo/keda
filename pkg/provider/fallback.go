package provider

import (
	"context"

	kedav1alpha1 "github.com/kedacore/keda/v2/api/v1alpha1"
	"github.com/kedacore/keda/v2/pkg/scalers"
	"k8s.io/api/autoscaling/v2beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/metrics/pkg/apis/external_metrics"
)

func isFallbackEnabled(scaledObject *kedav1alpha1.ScaledObject, metricSpec v2beta2.MetricSpec) bool {
	return scaledObject.Spec.Fallback != nil && metricSpec.External.Target.Type == v2beta2.AverageValueMetricType
}

func (p *KedaProvider) getMetricsWithFallback(scaler scalers.Scaler, metricName string, metricSelector labels.Selector, scaledObject *kedav1alpha1.ScaledObject, metricSpec v2beta2.MetricSpec) ([]external_metrics.ExternalMetricValue, error) {
	initHealthStatus(scaledObject)
	metrics, err := scaler.GetMetrics(context.TODO(), metricName, metricSelector)
	healthStatus := getHealthStatus(scaledObject, metricName)

	var m []external_metrics.ExternalMetricValue
	var e error

	switch {
	case err == nil:
		zero := uint32(0)
		healthStatus := getHealthStatus(scaledObject, metricName)
		healthStatus.NumberOfFailures = &zero
		healthStatus.Status = kedav1alpha1.HealthStatusHappy
		scaledObject.Status.Health[metricName] = *healthStatus

		m, e = metrics, nil
	case !isFallbackEnabled(scaledObject, metricSpec):
		healthStatus.Status = kedav1alpha1.HealthStatusFailing
		*healthStatus.NumberOfFailures += 1
		scaledObject.Status.Health[metricName] = *healthStatus

		m, e = nil, err
	case *healthStatus.NumberOfFailures >= scaledObject.Spec.Fallback.FailureThreshold:
		healthStatus.Status = kedav1alpha1.HealthStatusFailing
		*healthStatus.NumberOfFailures += 1
		scaledObject.Status.Health[metricName] = *healthStatus

		replicas := int64(scaledObject.Spec.Fallback.Replicas)
		normalisationValue, _ := metricSpec.External.Target.AverageValue.AsInt64()
		metric := external_metrics.ExternalMetricValue{
			MetricName: metricName,
			Value:      *resource.NewQuantity(normalisationValue*replicas, resource.DecimalSI),
			Timestamp:  metav1.Now(),
		}
		fallbackMetrics := []external_metrics.ExternalMetricValue{metric}

		m, e = fallbackMetrics, nil
	default:
		healthStatus.Status = kedav1alpha1.HealthStatusFailing
		*healthStatus.NumberOfFailures += 1
		scaledObject.Status.Health[metricName] = *healthStatus

		m, e = nil, err
	}

	p.updateStatus(scaledObject)
	return m, e
}

func (p *KedaProvider) updateStatus(scaledObject *kedav1alpha1.ScaledObject) {
	err := p.client.Status().Update(context.TODO(), scaledObject)
	if err != nil {
		logger.Info("Error updating ScaledObject status", err)
	}
}

func getHealthStatus(scaledObject *kedav1alpha1.ScaledObject, metricName string) *kedav1alpha1.HealthStatus {
	// Get health status for a specific metric
	_, healthStatusExists := scaledObject.Status.Health[metricName]
	if !healthStatusExists {
		zero := uint32(0)
		status := kedav1alpha1.HealthStatus{
			NumberOfFailures: &zero,
			Status:           kedav1alpha1.HealthStatusHappy,
		}
		scaledObject.Status.Health[metricName] = status
	}
	healthStatus := scaledObject.Status.Health[metricName]
	return &healthStatus
}

func initHealthStatus(scaledObject *kedav1alpha1.ScaledObject) {
	// Init health status if missing
	if scaledObject.Status.Health == nil {
		scaledObject.Status.Health = make(map[string]kedav1alpha1.HealthStatus)
	}
}
