package diff

import (
	"os"
	"path/filepath"
	"testing"
)

import (
	"github.com/stretchr/testify/assert"
)

const (
	TESTDATA_DIR = "testdata"
)

func TestDiff(t *testing.T) {
	// Assert different images with the same dimensions.
	diffFilePath1 := filepath.Join(os.TempDir(), "diff1.png")
	defer os.Remove(diffFilePath1)
	assertDiffs(t, "4029959456464745507", "16465366847175223174",
		&DiffMetrics{
			NumDiffPixels:     16,
			PixelDiffPercent:  0.0064,
			PixelDiffFilePath: diffFilePath1,
			MaxRGBDiffs:       []int{54, 100, 125},
			DimDiffer:         false})
	diffFilePath2 := filepath.Join(os.TempDir(), "diff2.png")
	defer os.Remove(diffFilePath2)
	assertDiffs(t, "5024150605949408692", "11069776588985027208",
		&DiffMetrics{
			NumDiffPixels:     2233,
			PixelDiffPercent:  0.8932,
			PixelDiffFilePath: diffFilePath2,
			MaxRGBDiffs:       []int{0, 0, 1},
			DimDiffer:         false})
	// Assert the same image.
	diffFilePath3 := filepath.Join(os.TempDir(), "diff3.png")
	defer os.Remove(diffFilePath3)
	assertDiffs(t, "5024150605949408692", "5024150605949408692",
		&DiffMetrics{
			NumDiffPixels:     0,
			PixelDiffPercent:  0,
			PixelDiffFilePath: diffFilePath3,
			MaxRGBDiffs:       []int{0, 0, 0},
			DimDiffer:         false})
	// Assert different images with different dimensions.
	diffFilePath4 := filepath.Join(os.TempDir(), "diff4.png")
	defer os.Remove(diffFilePath4)
	assertDiffs(t, "ffce5042b4ac4a57bd7c8657b557d495", "fffbcca7e8913ec45b88cc2c6a3a73ad",
		&DiffMetrics{
			NumDiffPixels:     571674,
			PixelDiffPercent:  89.324066,
			PixelDiffFilePath: diffFilePath4,
			MaxRGBDiffs:       []int{255, 255, 255},
			DimDiffer:         true})
	// Assert with images that match in dimensions but where all pixels differ.
	diffFilePath5 := filepath.Join(os.TempDir(), "diff5.png")
	defer os.Remove(diffFilePath5)
	assertDiffs(t, "4029959456464745507", "4029959456464745507-inverted",
		&DiffMetrics{
			NumDiffPixels:     250000,
			PixelDiffPercent:  100.0,
			PixelDiffFilePath: diffFilePath5,
			MaxRGBDiffs:       []int{255, 255, 255},
			DimDiffer:         false})

	// Assert different images where neither fits into the other.
	diffFilePath6 := filepath.Join(os.TempDir(), "diff5.png")
	defer os.Remove(diffFilePath6)
	assertDiffs(t, "fffbcca7e8913ec45b88cc2c6a3a73ad", "fffbcca7e8913ec45b88cc2c6a3a73ad-rotated",
		&DiffMetrics{
			NumDiffPixels:     172466,
			PixelDiffPercent:  74.8550347222,
			PixelDiffFilePath: diffFilePath6,
			MaxRGBDiffs:       []int{255, 255, 255},
			DimDiffer:         true})
	// Make sure the metric is symmetric.
	diffFilePath7 := filepath.Join(os.TempDir(), "diff6.png")
	defer os.Remove(diffFilePath7)
	assertDiffs(t, "fffbcca7e8913ec45b88cc2c6a3a73ad-rotated", "fffbcca7e8913ec45b88cc2c6a3a73ad",
		&DiffMetrics{
			NumDiffPixels:     172466,
			PixelDiffPercent:  74.8550347222,
			PixelDiffFilePath: diffFilePath7,
			MaxRGBDiffs:       []int{255, 255, 255},
			DimDiffer:         true})
}

func assertDiffs(t *testing.T, d1, d2 string, expectedDiffMetrics *DiffMetrics) {
	img1, err := OpenImage(filepath.Join(TESTDATA_DIR, d1+".png"))
	if err != nil {
		t.Fatal("Failed to open test file: ", err)
	}
	img2, err := OpenImage(filepath.Join(TESTDATA_DIR, d2+".png"))
	if err != nil {
		t.Fatal("Failed to open test file: ", err)
	}

	diffMetrics, err := Diff(img1, img2, expectedDiffMetrics.PixelDiffFilePath)
	if err != nil {
		t.Error("Unexpected error: ", err)
	}
	assert.Equal(t, expectedDiffMetrics, diffMetrics)
}
