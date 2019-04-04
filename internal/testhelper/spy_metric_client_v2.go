package testhelper

import (
	"code.cloudfoundry.org/loggregator-agent/pkg/metrics"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"sort"
	"sync"
)

type SpyMetricClientV2 struct {
	mu      sync.Mutex
	Metrics map[string]*SpyMetricV2
}

func NewMetricClientV2() *SpyMetricClientV2 {
	return &SpyMetricClientV2{
		Metrics: make(map[string]*SpyMetricV2),
	}
}

func (s *SpyMetricClientV2) NewCounter(name string, opts ...metrics.MetricOption) metrics.Counter {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := newSpyMetric(name, opts)
	s.addMetric(m)

	return m
}

func (s *SpyMetricClientV2) NewGauge(name string, opts ...metrics.MetricOption) metrics.Gauge {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := newSpyMetric(name, opts)
	s.addMetric(m)

	return m
}

func (s *SpyMetricClientV2) addMetric(sm *SpyMetricV2) {
	n := getMetricName(sm.name, sm.Opts.ConstLabels)

	s.Metrics[n] = sm
}

func (s *SpyMetricClientV2) GetMetric(name string, tags map[string]string) *SpyMetricV2 {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := getMetricName(name, tags)

	if m, ok := s.Metrics[n]; ok {
		return m
	}

	panic(fmt.Sprintf("unknown metric: %s", name))
}

func (s *SpyMetricClientV2) HasMetric(name string, tags map[string]string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := getMetricName(name, tags)
	_, ok := s.Metrics[n]
	return ok
}

func newSpyMetric(name string, opts []metrics.MetricOption) *SpyMetricV2 {
	sm := &SpyMetricV2{
		name: name,
		Opts: &prometheus.Opts{
			ConstLabels: make(prometheus.Labels),
		},
	}

	for _, o := range opts {
		o(sm.Opts)
	}

	for k, _ := range sm.Opts.ConstLabels {
		sm.keys = append(sm.keys, k)
	}
	sort.Strings(sm.keys)

	return sm
}

type SpyMetricV2 struct {
	mu    sync.Mutex
	value float64
	name  string

	keys []string
	Opts *prometheus.Opts
}

func (s *SpyMetricV2) Set(c float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = c
}

func (s *SpyMetricV2) Add(c float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value += c
}

func (s *SpyMetricV2) Value() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

func getMetricName(name string, tags map[string]string) string {
	n := name

	k := make([]string, len(tags))
	for t := range tags {
		k = append(k, t)
	}
	sort.Strings(k)

	for _, key := range k {
		n += fmt.Sprintf("%s_%s", key, tags[key])
	}

	return n
}
