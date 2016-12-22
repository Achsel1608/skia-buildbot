package metrics2

import "sync"

// meanInt64 returns the mean of the given slice of int64s.
func meanInt64(vals []interface{}) interface{} {
	if len(vals) == 0 {
		return int64(0)
	}
	t := int64(0)
	for _, v := range vals {
		t += v.(int64)
	}
	return t / int64(len(vals))
}

// aggregateMetric is a struct whose data is aggregated over the sampling period.
type aggregateMetric struct {
	aggFn       func([]interface{}) interface{}
	client      *influxClient
	key         string
	measurement string
	mtx         sync.RWMutex
	tags        map[string]string
	values      []interface{}
}

// get returns the aggregation of the values stored in the metric.
func (m *aggregateMetric) get() interface{} {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.aggFn(m.values)
}

// update adds a new value to the metric.
func (m *aggregateMetric) update(v interface{}) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.values = append(m.values, v)
}

// reset returns the aggregation of the values stored in the metric and clears them.
func (m *aggregateMetric) reset() interface{} {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	rv := m.aggFn(m.values)
	m.values = []interface{}{}
	return rv
}

// Delete removes the metric from its Client's registry.
func (m *aggregateMetric) Delete() error {
	m.mtx.Lock()
	client := m.client
	key := m.key
	// Release m.mtx before calling Client.deleteAggregateMetric() to prevent
	// deadlock.
	//   - Client.collectAggregateMetrics() (called periodically from goroutine in
	//     NewClient()) locks Client.aggMetricsMtx while calling
	//     aggregateMetric.reset(), which locks aggregateMetric.mtx.
	//   - If we don't unlock aggregateMetric.mtx here, we will be holding it when
	//     Client.deleteAggregateMetric() locks Client.aggMetricsMtx.
	m.mtx.Unlock()
	return client.deleteAggregateMetric(key)
}

// int64MeanMetric is a metric whose data is aggregated over the sampling period
// using an arithmetic mean.
type int64MeanMetric struct {
	*aggregateMetric
}

// getInt64MeanMetric returns an int64MeanMetric instance.
func (c *influxClient) getInt64MeanMetric(measurement string, tags ...map[string]string) *int64MeanMetric {
	return &int64MeanMetric{
		c.getAggregateMetric(measurement, tags, meanInt64),
	}
}
