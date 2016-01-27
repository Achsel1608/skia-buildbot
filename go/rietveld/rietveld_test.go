package rietveld

import (
	"testing"
	"time"

	assert "github.com/stretchr/testify/require"
	"go.skia.org/infra/go/testutils"
)

// Basic test to make sure we can retrieve issues from Rietveld.
func TestRietveld(t *testing.T) {
	testutils.SkipIfShort(t)

	api := New("https://codereview.chromium.org", nil)
	t_delta := time.Now().Add(-10 * 24 * time.Hour)
	issues, err := api.Search(1, SearchModifiedAfter(t_delta))
	assert.Nil(t, err)
	assert.True(t, len(issues) > 0)

	for _, issue := range issues {
		assert.True(t, issue.Modified.After(t_delta))
		details, err := api.GetIssueProperties(issue.Issue, false)
		assert.Nil(t, err)
		assert.True(t, details.Modified.After(t_delta))
		assert.True(t, len(details.Patchsets) > 0)
	}

	keys, err := api.SearchKeys(5, SearchModifiedAfter(time.Now().Add(-time.Hour)))
	assert.Nil(t, err)
	assert.Equal(t, 5, len(keys))
}
