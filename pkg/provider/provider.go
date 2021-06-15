package provider

import (
	"context"
	"fmt"
	"github.com/kedacore/keda/v2/pkg/scalers"
	"k8s.io/api/autoscaling/v2beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"

	kedav1alpha1 "github.com/kedacore/keda/v2/api/v1alpha1"
	prommetrics "github.com/kedacore/keda/v2/pkg/metrics"
	"github.com/kedacore/keda/v2/pkg/scaling"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/custom-metrics-apiserver/pkg/provider"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/metrics/pkg/apis/custom_metrics"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KedaProvider implements External Metrics Provider
type KedaProvider struct {
	client           client.Client
	values           map[provider.CustomMetricInfo]int64
	externalMetrics  []externalMetric
	scaleHandler     scaling.ScaleHandler
	watchedNamespace string
}

type externalMetric struct{}

var logger logr.Logger
var metricsServer prommetrics.PrometheusMetricServer

// NewProvider returns an instance of KedaProvider
func NewProvider(adapterLogger logr.Logger, scaleHandler scaling.ScaleHandler, client client.Client, watchedNamespace string) provider.MetricsProvider {
	provider := &KedaProvider{
		values:           make(map[provider.CustomMetricInfo]int64),
		externalMetrics:  make([]externalMetric, 2, 10),
		client:           client,
		scaleHandler:     scaleHandler,
		watchedNamespace: watchedNamespace,
	}
	logger = adapterLogger.WithName("provider")
	logger.Info("starting")
	return provider
}

// GetExternalMetric retrieves metrics from the scalers
// Metric is normally identified by a name and a set of labels/tags. It is up to a specific
// implementation how to translate metricSelector to a filter for metric values.
// Namespace can be used by the implementation for metric identification, access control or ignored.
func (p *KedaProvider) GetExternalMetric(namespace string, metricSelector labels.Selector, info provider.ExternalMetricInfo) (*external_metrics.ExternalMetricValueList, error) {
	// Note:
	//		metric name and namespace is used to lookup for the CRD which contains configuration to call azure
	// 		if not found then ignored and label selector is parsed for all the metrics
	logger.V(1).Info("Keda provider received request for external metrics", "namespace", namespace, "metric name", info.Metric, "metricSelector", metricSelector.String())
	selector, err := labels.ConvertSelectorToLabelsMap(metricSelector.String())
	if err != nil {
		logger.Error(err, "Error converting Selector to Labels Map")
		return nil, err
	}

	// get the scaled objects matching namespace and labels
	scaledObjects := &kedav1alpha1.ScaledObjectList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels(selector),
	}
	err = p.client.List(context.TODO(), scaledObjects, opts...)
	if err != nil {
		return nil, err
	} else if len(scaledObjects.Items) != 1 {
		return nil, fmt.Errorf("exactly one scaled object should match label %s", metricSelector.String())
	}

	scaledObject := &scaledObjects.Items[0]
	matchingMetrics := []external_metrics.ExternalMetricValue{}
	scalers, err := p.scaleHandler.GetScalers(scaledObject)
	metricsServer.RecordScalerObjectError(scaledObject.Namespace, scaledObject.Name, err)
	if err != nil {
		return nil, fmt.Errorf("error when getting scalers %s", err)
	}

	for scalerIndex, scaler := range scalers {
		metricSpecs := scaler.GetMetricSpecForScaling()
		scalerName := strings.Replace(fmt.Sprintf("%T", scaler), "*scalers.", "", 1)

		for _, metricSpec := range metricSpecs {
			// skip cpu/memory resource scaler
			if metricSpec.External == nil {
				continue
			}
			// Filter only the desired metric
			if strings.EqualFold(metricSpec.External.Metric.Name, info.Metric) {
				metrics, err := getMetricsWithFallback(scaler, info, metricSelector, scaledObject, metricSpec)

				if err != nil {
					logger.Error(err, "error getting metric for scaler", "scaledObject.Namespace", scaledObject.Namespace, "scaledObject.Name", scaledObject.Name, "scaler", scaler)
				} else {
					for _, metric := range metrics {
						metricValue, _ := metric.Value.AsInt64()
						metricsServer.RecordHPAScalerMetric(namespace, scaledObject.Name, scalerName, scalerIndex, metric.MetricName, metricValue)
					}
					matchingMetrics = append(matchingMetrics, metrics...)
				}
				metricsServer.RecordHPAScalerError(namespace, scaledObject.Name, scalerName, scalerIndex, info.Metric, err)
			}
		}
		logger.Info("Banana directly before status update")
		p.client.Status().Update(context.TODO(), scaledObject)
		scaler.Close()
	}

	if len(matchingMetrics) == 0 {
		return nil, fmt.Errorf("No matching metrics found for " + info.Metric)
	}

	return &external_metrics.ExternalMetricValueList{
		Items: matchingMetrics,
	}, nil
}

func isFallbackActive() bool {
	return true
}

func getMetricsWithFallback(scaler scalers.Scaler, info provider.ExternalMetricInfo, metricSelector labels.Selector, scaledObject *kedav1alpha1.ScaledObject, metricSpec v2beta2.MetricSpec) ([]external_metrics.ExternalMetricValue, error) {
	initFallbackStatus(scaledObject)
	metrics, err := scaler.GetMetrics(context.TODO(), info.Metric, metricSelector)
	if err == nil {
		if isFallbackActive() {
			// Reset the fallback status
			zero := uint32(0)
			fallbackStatus := getFallbackStatus(scaledObject, info)
			fallbackStatus.NumberOfFailures = &zero
			fallbackStatus.Status = kedav1alpha1.FallbackStatusHappy
			scaledObject.Status.Fallback[info.Metric] = fallbackStatus
		}
		return metrics, nil
	}

	if !isFallbackActive() {
		return nil, err
	}
	fallbackStatus := getFallbackStatus(scaledObject, info)

	if *fallbackStatus.NumberOfFailures > *scaledObject.Spec.Fallback.FailureThreshold {
		fallbackStatus.Status = kedav1alpha1.FallbackStatusFailing
	} else {
		fallbackStatus.Status = kedav1alpha1.FallbackStatusPending
		*fallbackStatus.NumberOfFailures += 1
	}
	if metricSpec.External.Target.Type == v2beta2.AverageValueMetricType {
		replicas := int64(*scaledObject.Spec.Fallback.FallbackReplicas)
		normalisationValue, _ := metricSpec.External.Target.AverageValue.AsInt64()
		metric := external_metrics.ExternalMetricValue{
			MetricName: info.Metric,
			Value:      *resource.NewQuantity(normalisationValue*replicas, resource.DecimalSI),
			Timestamp:  metav1.Now(),
		}
		fallbackMetrics := []external_metrics.ExternalMetricValue{metric}
		return fallbackMetrics, nil
	}
	return nil, err
}

func getFallbackStatus(scaledObject *kedav1alpha1.ScaledObject, info provider.ExternalMetricInfo) kedav1alpha1.FallbackStatus {
	// Get fallback status for specific metric
	_, fallbackStatusExists := scaledObject.Status.Fallback[info.Metric]
	if !fallbackStatusExists {
		logger.Info("Banana fallbackStatus doesnt exists, creating ")
		zero := uint32(0)
		initStatus := kedav1alpha1.FallbackStatus{
			NumberOfFailures: &zero,
			Status:           kedav1alpha1.FallbackStatusHappy,
		}
		scaledObject.Status.Fallback[info.Metric] = initStatus
	}
	fallbackStatus := scaledObject.Status.Fallback[info.Metric]
	return fallbackStatus
}

func initFallbackStatus(scaledObject *kedav1alpha1.ScaledObject) {
	// Init fallback status if missing
	if scaledObject.Status.Fallback == nil {
		logger.Info("Banana my fallbackStatus is nil")
		scaledObject.Status.Fallback = make(map[string]kedav1alpha1.FallbackStatus)
	}
}

// ListAllExternalMetrics returns the supported external metrics for this provider
func (p *KedaProvider) ListAllExternalMetrics() []provider.ExternalMetricInfo {
	externalMetricsInfo := []provider.ExternalMetricInfo{}

	// get all ScaledObjects in namespace(s) watched by the operator
	scaledObjects := &kedav1alpha1.ScaledObjectList{}
	opts := []client.ListOption{
		client.InNamespace(p.watchedNamespace),
	}
	err := p.client.List(context.TODO(), scaledObjects, opts...)
	if err != nil {
		logger.Error(err, "Cannot get list of ScaledObjects", "WatchedNamespace", p.watchedNamespace)
		return nil
	}

	// get metrics from all watched ScaledObjects
	for _, scaledObject := range scaledObjects.Items {
		for _, metric := range scaledObject.Status.ExternalMetricNames {
			externalMetricsInfo = append(externalMetricsInfo, provider.ExternalMetricInfo{Metric: metric})
		}
	}
	return externalMetricsInfo
}

// GetMetricByName fetches a particular metric for a particular object.
// The namespace will be empty if the metric is root-scoped.
func (p *KedaProvider) GetMetricByName(name types.NamespacedName, info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValue, error) {
	// not implemented yet
	return nil, apiErrors.NewServiceUnavailable("not implemented yet")
}

// GetMetricBySelector fetches a particular metric for a set of objects matching
// the given label selector.  The namespace will be empty if the metric is root-scoped.
func (p *KedaProvider) GetMetricBySelector(namespace string, selector labels.Selector, info provider.CustomMetricInfo, metricSelector labels.Selector) (*custom_metrics.MetricValueList, error) {
	logger.V(0).Info("Received request for custom metric", "groupresource", info.GroupResource.String(), "namespace", namespace, "metric name", info.Metric, "selector", selector.String())
	return nil, apiErrors.NewServiceUnavailable("not implemented yet")
}

// ListAllMetrics provides a list of all available metrics at
// the current time.  Note that this is not allowed to return
// an error, so it is recommended that implementors cache and
// periodically update this list, instead of querying every time.
func (p *KedaProvider) ListAllMetrics() []provider.CustomMetricInfo {
	// not implemented yet
	return []provider.CustomMetricInfo{}
}
