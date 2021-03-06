package config

import (
	"path/filepath"
	"testing"

	"go.skia.org/infra/go/testutils"

	assert "github.com/stretchr/testify/require"
)

func TestConfigRead(t *testing.T) {
	testutils.SmallTest(t)
	m, err := ReadMetrics(filepath.Join("./testdata", "metrics.cfg"))
	assert.NoError(t, err)
	assert.Equal(t, 2, len(m))
	assert.Equal(t, "qps", m[0].Name)
	assert.Equal(t, "fiddle-sec-violations", m[1].Name)
}
