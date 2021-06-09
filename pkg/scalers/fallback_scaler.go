package scalers

import (
	"context"
	"fmt"
	"strconv"

	"k8s.io/api/autoscaling/v2beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/metrics/pkg/apis/external_metrics"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kedautil "github.com/kedacore/keda/v2/pkg/util"
)

const (
	fallbackMetricType = "External"
)

type fallbackScaler struct {
	fallbackReplicas uint32
	delegate         *Scaler
}

// type delegateConfig struct {
// 	triggerType string
// 	metadata    map[string]string
// }

// type fallbackMetadata struct {
// 	fallbackReplicas int64
// 	delegate         ScalerConfig
// }

var fallbackLog = logf.Log.WithName("fallback_scaler")

// NewCronScaler creates a new cronScaler
func NewFallbackScaler(fallbackReplicas *uint32, scaler *Scaler) Scaler {

	return &fallbackScaler{
		fallbackReplicas: *fallbackReplicas,
		delegate:         scaler,
	}
}


// IsActive checks if the startTime or endTime has reached
func (s *fallbackScaler) IsActive(ctx context.Context) (bool, error) {
	return (*s.delegate).IsActive(ctx)
}

func (s *fallbackScaler) Close() error {
	return (*s.delegate).Close()
}

// GetMetricSpecForScaling returns the metric spec for the HPA
func (s *fallbackScaler) GetMetricSpecForScaling() []v2beta2.MetricSpec {
	return (*s.delegate).GetMetricSpecForScaling()
}

// GetMetrics finds the current value of the metric
func (s *fallbackScaler) GetMetrics(ctx context.Context, metricName string, metricSelector labels.Selector) ([]external_metrics.ExternalMetricValue, error) {
	metric, err := (*s.delegate).GetMetrics(ctx, metricName, metricSelector)
	if err != nil {

	} else {
		return metric, err
	}

	var currentReplicas = int64(defaultDesiredReplicas)
	isActive, err := s.IsActive(ctx)
	if err != nil {
		cronLog.Error(err, "error")
		return []external_metrics.ExternalMetricValue{}, err
	}
	if isActive {
		s.GetMetricSpecForScaling()
		currentReplicas = s.metadata.desiredReplicas
	}

	/*******************************************************************************/
	metric := external_metrics.ExternalMetricValue{
		MetricName: metricName,
		Value:      *resource.NewQuantity(currentReplicas, resource.DecimalSI),
		Timestamp:  metav1.Now(),
	}

	return append([]external_metrics.ExternalMetricValue{}, metric), nil
}
