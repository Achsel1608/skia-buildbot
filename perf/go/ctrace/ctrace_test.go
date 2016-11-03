package ctrace

import (
	"math"
	"testing"

	"go.skia.org/infra/go/testutils"
	"go.skia.org/infra/perf/go/config"
	"go.skia.org/infra/perf/go/kmeans"
)

func near(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}

func TestDistance(t *testing.T) {
	testutils.SmallTest(t)
	a := &ClusterableTrace{Values: []float64{3, 0}}
	b := &ClusterableTrace{Values: []float64{0, 4}}
	if got, want := a.Distance(b), 5.0; !near(got, want) {
		t.Errorf("Distance mismatch: Got %f Want %f", got, want)
	}
	if got, want := a.Distance(a), 0.0; !near(got, want) {
		t.Errorf("Distance mismatch: Got %f Want %f", got, want)
	}
}

func TestNewFullTraceKey(t *testing.T) {
	testutils.SmallTest(t)
	ct := NewFullTrace("foo", []float64{1, -1}, map[string]string{"foo": "bar"}, config.MIN_STDDEV)
	if got, want := ct.Key, "foo"; got != want {
		t.Errorf("Key not set: Got %s Want %s", got, want)
	}
	if got, want := ct.Params["foo"], "bar"; got != want {
		t.Errorf("Params not set: Got %s Want %s", got, want)
	}
}

func TestNewFullTrace(t *testing.T) {
	testutils.SmallTest(t)
	// All positive (Near=true) testcases should end up with a normalized array
	// of values with 1.0 in the first spot and a standard deviation of 1.0.
	testcases := []struct {
		Values []float64
		Near   bool
	}{
		{
			Values: []float64{1.0, -1.0},
			Near:   true,
		},
		{
			Values: []float64{1e100, 1.0, -1.0, -1.0},
			Near:   true,
		},
		{
			Values: []float64{1e100, 1.0, -1.0, 1e100},
			Near:   true,
		},
		{
			Values: []float64{1e100, 2.0, -2.0, 1e100},
			Near:   true,
		},
		{
			// There's a limit to how small of a stddev we will normalize.
			Values: []float64{1e100, config.MIN_STDDEV, -config.MIN_STDDEV, 1e100},
			Near:   false,
		},
	}
	for _, tc := range testcases {
		ct := NewFullTrace("foo", tc.Values, map[string]string{}, config.MIN_STDDEV)
		if got, want := ct.Values[0], 1.0; near(got, want) != tc.Near {
			t.Errorf("Normalization failed for values %#v: near(Got %f, Want %f) != %t", tc.Values, got, want, tc.Near)
		}
	}
}

func TestCalculateCentroid(t *testing.T) {
	testutils.SmallTest(t)
	members := []kmeans.Clusterable{
		&ClusterableTrace{Values: []float64{4, 0}},
		&ClusterableTrace{Values: []float64{0, 8}},
	}
	c := CalculateCentroid(members).(*ClusterableTrace)
	if got, want := c.Values[0], 2.0; !near(got, want) {
		t.Errorf("Failed calculating centroid: !near(Got %f, Want %f)", got, want)
	}
	if got, want := c.Values[1], 4.0; !near(got, want) {
		t.Errorf("Failed calculating centroid: !near(Got %f, Want %f)", got, want)
	}

}
