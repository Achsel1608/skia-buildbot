// ctrace makes Traces into ClusterableTraces which can then be used in kmeans.
package ctrace

import (
	"fmt"
	"math"

	"go.skia.org/infra/perf/go/kmeans"
	"go.skia.org/infra/perf/go/vec"
)

// ClusterableTrace contains Trace data and implements kmeans.Clusterable and kmeans.Centroid.
type ClusterableTrace struct {
	Key    string
	Values []float64
	Params map[string]string
}

// See kmeans.Centroid.
func (t *ClusterableTrace) Distance(c kmeans.Clusterable) float64 {
	// Data is always loaded from tiles so that every Trace has the same length,
	// and NewFullTrace keeps that guarantee.
	o := c.(*ClusterableTrace)
	sum := 0.0
	for i, x := range t.Values {
		sum += (x - o.Values[i]) * (x - o.Values[i])
	}
	return math.Sqrt(sum)
}

// See kmeans.Centroid.
func (t *ClusterableTrace) AsClusterable() kmeans.Clusterable {
	return t
}

func (t *ClusterableTrace) String() string {
	return fmt.Sprintf("%s %#v", t.Key, t.Values[:2])
}

// NewFullTrace takes data you would find in a Trace and returns a
// ClusterableTrace usable for kmeans clustering.
func NewFullTrace(key string, values []float64, params map[string]string, minStdDev float64) *ClusterableTrace {
	norm := make([]float64, len(values))

	copy(norm, values)
	vec.Fill(norm)
	vec.Norm(norm, minStdDev)

	return &ClusterableTrace{
		Key:    key,
		Values: norm,
		Params: params,
	}
}

// CalculateCentroid implements kmeans.CalculateCentroid.
func CalculateCentroid(members []kmeans.Clusterable) kmeans.Centroid {
	first := members[0].(*ClusterableTrace)
	mean := make([]float64, len(first.Values))
	for _, m := range members {
		ft := m.(*ClusterableTrace)
		for i, x := range ft.Values {
			mean[i] += x
		}
	}
	numMembers := float64(len(members))
	for i, _ := range mean {
		mean[i] = mean[i] / numMembers
	}
	return &ClusterableTrace{
		Key:    "I'm a centroid!",
		Values: mean,
	}
}
