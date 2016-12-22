package metrics2

/*
   Convenience utilities for working with InfluxDB.
*/

import (
	"fmt"
	"os"
	"sync"
	"time"

	"go.skia.org/infra/go/influxdb"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/util"
)

const (
	DEFAULT_REPORT_FREQUENCY = time.Minute
	PUSH_FREQUENCY           = time.Minute
)

// Timer is a struct used for measuring elapsed time. Unlike the other metrics
// helpers, timer does not continuously report data; instead, it reports a
// single data point when Stop() is called.
type Timer interface {
	// Start starts or resets the timer.
	Start()

	// Stop stops the timer and reports the elapsed time.
	Stop()
}

// Liveness keeps a time-since-last-successful-update metric.
//
// The unit of the metrics is in seconds.
//
// It is used to keep track of periodic processes to make sure that they are running
// successfully. Every liveness metric should have a corresponding alert set up that
// will fire of the time-since-last-successful-update metric gets too large.
type Liveness interface {
	// Delete removes the Liveness from metrics.
	Delete() error

	// Get returns the current value of the Liveness.
	Get() int64

	// ManualReset sets the last-successful-update time of the Liveness to a specific value. Useful for tracking processes whose lifetimes are outside of that of the current process, but should not be needed in most cases.
	ManualReset(lastSuccessfulUpdate time.Time)

	// Reset should be called when some work has been successfully completed.
	Reset()
}

// Int64Metric is a metric which reports an int64 value.
type Int64Metric interface {
	// Delete removes the metric from its Client's registry.
	Delete() error

	// Get returns the current value of the metric.
	Get() int64

	// Update adds a data point to the metric.
	Update(v int64)
}

// Float64Metric is a metric which reports a float64 value.
type Float64Metric interface {
	// Delete removes the metric from its Client's registry.
	Delete() error

	// Get returns the current value of the metric.
	Get() float64

	// Update adds a data point to the metric.
	Update(v float64)
}

// Counter is a struct used for tracking metrics which increment or decrement.
type Counter interface {
	// Dec decrements the counter by the given quantity.
	Dec(i int64)

	// Delete removes the counter from metrics.
	Delete() error

	// Get returns the current value in the counter.
	Get() int64

	// Inc increments the counter by the given quantity.
	Inc(i int64)

	// Reset sets the counter to zero.
	Reset()
}

// BoolMetric is a metric which reports a boolean value.
type BoolMetric interface {
	// Delete removes the metric from its Client's registry.
	Delete() error

	// Get returns the current value of the metric.
	Get() bool

	// Update adds a data point to the metric.
	Update(v bool)
}

// Client represents a set of metrics.
type Client interface {
	// Flush pushes any queued data immediately. Long running apps shouldn't worry about this as Client will auto-push every so often.
	Flush() error

	// GetBoolMetric returns a BoolMetric instance.
	GetBoolMetric(measurement string, tags ...map[string]string) BoolMetric

	// GetCounter creates or retrieves a Counter with the given name and tag set and returns it.
	GetCounter(name string, tagsList ...map[string]string) Counter

	// GetFloat64Metric returns a Float64Metric instance.
	GetFloat64Metric(measurement string, tags ...map[string]string) Float64Metric

	// GetInt64Metric returns an Int64Metric instance.
	GetInt64Metric(measurement string, tags ...map[string]string) Int64Metric

	// NewLiveness creates a new Liveness metric helper.
	NewLiveness(name string, tagsList ...map[string]string) Liveness

	// NewTimer creates and returns a new started timer.
	NewTimer(name string, tagsList ...map[string]string) Timer
}

var (
	defaultInfluxClient *influxClient = &influxClient{
		aggMetrics:      map[string]*aggregateMetric{},
		counters:        map[string]*counter{},
		metrics:         map[string]*rawMetric{},
		reportFrequency: time.Minute,
	}
	defaultClient Client = defaultInfluxClient
)

// GetDefaultClient returns the default Client.
func GetDefaultClient() Client {
	return defaultClient
}

// Init() initializes the metrics package.
func Init(appName string, influxDbClient *influxdb.Client) error {
	hostName, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("Unable to retrieve hostname: %s", err)
	}
	tags := map[string]string{
		"app":  appName,
		"host": hostName,
	}
	clientI, err := NewClient(influxDbClient, tags, DEFAULT_REPORT_FREQUENCY)
	if err != nil {
		return err
	}
	c := clientI.(*influxClient)
	// Some metrics may already be registered with defaultInfluxClient. Copy them
	// over.
	c.aggMetrics = defaultInfluxClient.aggMetrics
	c.counters = defaultInfluxClient.counters
	c.metrics = defaultInfluxClient.metrics

	// Set the default client.
	defaultClient = c
	defaultInfluxClient = c
	return nil
}

// influxClient is a struct used for communicating with an InfluxDB instance.
//
// It implements Client.
type influxClient struct {
	aggMetrics    map[string]*aggregateMetric
	aggMetricsMtx sync.Mutex

	counters    map[string]*counter
	countersMtx sync.Mutex

	influxDbClient *influxdb.Client
	defaultTags    map[string]string

	metrics    map[string]*rawMetric
	metricsMtx sync.Mutex

	reportFrequency time.Duration
	values          *influxdb.BatchPoints
	valuesMtx       sync.Mutex
}

// NewClient returns a Client which uses the given influxdb.Client to push data.
// defaultTags specifies a set of default tag keys and values which are applied
// to all data points. reportFrequency specifies how often metrics should create
// data points.
func NewClient(influxDbClient *influxdb.Client, defaultTags map[string]string, reportFrequency time.Duration) (Client, error) {
	values, err := influxDbClient.NewBatchPoints()
	if err != nil {
		return nil, err
	}
	c := &influxClient{
		aggMetrics:      map[string]*aggregateMetric{},
		counters:        map[string]*counter{},
		influxDbClient:  influxDbClient,
		defaultTags:     defaultTags,
		metrics:         map[string]*rawMetric{},
		reportFrequency: reportFrequency,
		values:          values,
	}
	go func() {
		for _ = range time.Tick(PUSH_FREQUENCY) {
			byMeasurement, err := c.pushData()
			if err != nil {
				sklog.Errorf("Failed to push data into InfluxDB: %s", err)
			} else {
				total := int64(0)
				for k, v := range byMeasurement {
					c.GetInt64Metric("metrics.points-pushed.by-measurement", map[string]string{"measurement": k}).Update(v)
					total += v
				}
				c.GetInt64Metric("metrics.points-pushed.total", nil).Update(total)
			}
		}
	}()
	go func() {
		for _ = range time.Tick(reportFrequency) {
			c.collectMetrics()
			c.collectAggregateMetrics()
		}
	}()
	return c, nil
}

// collectMetrics collects data points from all raw metrics.
func (c *influxClient) collectMetrics() {
	c.metricsMtx.Lock()
	defer c.metricsMtx.Unlock()
	for _, m := range c.metrics {
		c.addPoint(m.measurement, m.tags, m.get())
	}
}

// collectAggregateMetrics collects data points from all aggregate metrics.
func (c *influxClient) collectAggregateMetrics() {
	c.aggMetricsMtx.Lock()
	defer c.aggMetricsMtx.Unlock()
	for _, m := range c.aggMetrics {
		c.addPoint(m.measurement, m.tags, m.reset())
	}
}

// addPointAtTime adds a data point with the given timestamp.
func (c *influxClient) addPointAtTime(measurement string, tags map[string]string, value interface{}, ts time.Time) {
	c.valuesMtx.Lock()
	defer c.valuesMtx.Unlock()
	if c.values == nil {
		sklog.Errorf("Metrics client not initialized; cannot add points.")
		return
	}
	if tags == nil {
		tags = map[string]string{}
	}
	allTags := make(map[string]string, len(tags)+len(c.defaultTags))
	for k, v := range c.defaultTags {
		allTags[k] = v
	}
	for k, v := range tags {
		allTags[k] = v
	}
	if err := c.values.AddPoint(measurement, allTags, map[string]interface{}{"value": value}, ts); err != nil {
		sklog.Errorf("Failed to add data point: %s", err)
	}
}

// addPoint adds a data point.
func (c *influxClient) addPoint(measurement string, tags map[string]string, value interface{}) {
	c.addPointAtTime(measurement, tags, value, time.Now())
}

// RawAddInt64PointAtTime adds an int64 data point to the default client at the
// given time. When possible, use one of the helpers instead.
//
// TODO(jcgregorio) This func should be removed.
func RawAddInt64PointAtTime(measurement string, tags map[string]string, value int64, ts time.Time) {
	// This is the only place where defaultInfluxClient is used. Once this func
	// is removed then defaultInfluxClient can also be removed.
	if defaultInfluxClient != nil {
		defaultInfluxClient.addPointAtTime(measurement, tags, value, ts)
	}
}

// pushData pushes all queued data into InfluxDB.
func (c *influxClient) pushData() (map[string]int64, error) {
	c.valuesMtx.Lock()
	defer c.valuesMtx.Unlock()

	// Always clear out the values after pushing, even if we failed.
	newValues, err := c.influxDbClient.NewBatchPoints()
	if err != nil {
		return nil, err
	}
	defer func() {
		c.values = newValues
	}()

	if c.influxDbClient == nil {
		return nil, fmt.Errorf("InfluxDB client is nil! Cannot push data. Did you initialize the metrics2 package?")
	}

	// Push the points.
	if err := c.influxDbClient.WriteBatch(c.values); err != nil {
		return nil, err
	}

	// Record the number of points.
	byMeasurement := map[string]int64{}
	points := c.values.Points()
	for _, pt := range points {
		count := byMeasurement[pt.Name()]
		byMeasurement[pt.Name()] = count + 1
	}

	return byMeasurement, nil
}

// Flush pushes any queued data into InfluxDB immediately. Long running apps shouldn't worry about this as Client will auto-push every so often.
func (c *influxClient) Flush() error {
	c.collectMetrics()
	c.collectAggregateMetrics()
	if _, err := c.pushData(); err != nil {
		return err
	}
	return nil
}

// Flush pushes any queued data into InfluxDB.  It is meant to be deferred by short running apps.  Long running apps shouldn't worry about this as metrics2 will auto-push every so often.
func Flush() {
	if err := defaultClient.Flush(); err != nil {
		sklog.Errorf("There was a problem while flushing metrics: %s", err)
	}
}

// rawMetric is a metric which has no explicit type.
type rawMetric struct {
	client      *influxClient
	key         string
	measurement string
	mtx         sync.RWMutex
	tags        map[string]string
	value       interface{}
}

// get returns the current value of the metric.
func (m *rawMetric) get() interface{} {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	return m.value
}

// update adds a data point to the metric.
func (m *rawMetric) update(v interface{}) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.value = v
}

// Delete removes the metric from its Client's registry.
func (m *rawMetric) Delete() error {
	m.mtx.Lock()
	key := m.key
	client := m.client
	// Release m.mtx before calling Client.deleteRawMetric() to prevent deadlock.
	//   - Client.collectMetrics() (called periodically from goroutine in
	//     NewClient()) locks Client.metricsMtx while calling rawMetric.get(),
	//     which locks rawMetric.mtx.
	//   - If we don't unlock rawMetric.mtx here, we will be holding it when
	//     Client.deleteRawMetric() locks Client.metricsMtx.
	m.mtx.Unlock()
	return client.deleteRawMetric(key)
}

// makeMetricKey generates a key for the given metric based on its measurement
// name and tags.
func makeMetricKey(measurement string, tags map[string]string) string {
	md5, err := util.MD5Params(tags)
	if err != nil {
		sklog.Errorf("Failed to encode measurement tags: %s", err)
	}
	return fmt.Sprintf("%s_%s", measurement, md5)
}

// getRawMetric creates or retrieves a metric with the given measurement name
// and tag set and returns it.
func (c *influxClient) getRawMetric(measurement string, tagsList []map[string]string, initial interface{}) *rawMetric {
	c.metricsMtx.Lock()
	defer c.metricsMtx.Unlock()

	// Make a copy of the concatenation of all provided tags.
	tags := util.AddParams(map[string]string{}, tagsList...)
	key := makeMetricKey(measurement, tags)
	m, ok := c.metrics[key]
	if !ok {
		m = &rawMetric{
			client:      c,
			key:         key,
			measurement: measurement,
			tags:        tags,
			value:       initial,
		}
		c.metrics[key] = m
	}
	return m
}

// getAggregateMetric creates or retrieves an aggregateMetric with the given
// measurement name and tag set and returns it.
func (c *influxClient) getAggregateMetric(measurement string, tagsList []map[string]string, aggFn func([]interface{}) interface{}) *aggregateMetric {
	c.aggMetricsMtx.Lock()
	defer c.aggMetricsMtx.Unlock()

	// Make a copy of the concatenation of all provided tags.
	tags := util.AddParams(map[string]string{}, tagsList...)
	key := makeMetricKey(measurement, tags)
	m, ok := c.aggMetrics[key]
	if !ok {
		m = &aggregateMetric{
			aggFn:       aggFn,
			client:      c,
			key:         key,
			measurement: measurement,
			tags:        tags,
			values:      []interface{}{},
		}
		c.aggMetrics[key] = m
	}
	return m
}

// deleteRawMetric removes the given raw metric.
func (c *influxClient) deleteRawMetric(key string) error {
	c.metricsMtx.Lock()
	defer c.metricsMtx.Unlock()

	if _, ok := c.metrics[key]; ok {
		delete(c.metrics, key)
	} else {
		return fmt.Errorf("Unable to delete unknown metric: %s", key)
	}
	return nil
}

// deleteAggregateMetric removes the given aggregate metric.
func (c *influxClient) deleteAggregateMetric(key string) error {
	c.aggMetricsMtx.Lock()
	defer c.aggMetricsMtx.Unlock()

	if _, ok := c.aggMetrics[key]; ok {
		delete(c.aggMetrics, key)
	} else {
		return fmt.Errorf("Unable to delete unknown metric: %s", key)
	}
	return nil
}

// Validate that the concrete structs faithfully implement their respective interfaces.
var _ Client = (*influxClient)(nil)
var _ BoolMetric = (*boolMetric)(nil)
var _ Counter = (*counter)(nil)
var _ Float64Metric = (*float64Metric)(nil)
var _ Int64Metric = (*int64Metric)(nil)
var _ Liveness = (*liveness)(nil)
var _ Timer = (*timer)(nil)
