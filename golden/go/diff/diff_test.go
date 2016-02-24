package diff

import (
	"bytes"
	"image"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/skia-dev/glog"
	"github.com/stretchr/testify/assert"
	"go.skia.org/infra/golden/go/image/text"
)

const (
	TESTDATA_DIR = "testdata"
)

func TestDiffMetrics(t *testing.T) {
	// Assert different images with the same dimensions.
	assertDiffs(t, "4029959456464745507", "16465366847175223174",
		&DiffMetrics{
			NumDiffPixels:     16,
			PixelDiffPercent:  0.0064,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{13878, 25700, 32125, 0},
			DimDiffer:         false})
	assertDiffs(t, "5024150605949408692", "11069776588985027208",
		&DiffMetrics{
			NumDiffPixels:     2233,
			PixelDiffPercent:  0.8932,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{0, 0, 257, 0},
			DimDiffer:         false})
	// Assert the same image.
	assertDiffs(t, "5024150605949408692", "5024150605949408692",
		&DiffMetrics{
			NumDiffPixels:     0,
			PixelDiffPercent:  0,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{0, 0, 0, 0},
			DimDiffer:         false})
	// Assert different images with different dimensions.
	assertDiffs(t, "ffce5042b4ac4a57bd7c8657b557d495", "fffbcca7e8913ec45b88cc2c6a3a73ad",
		&DiffMetrics{
			NumDiffPixels:     571674,
			PixelDiffPercent:  89.324066,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{65535, 65535, 65535, 0},
			DimDiffer:         true})
	// Assert with images that match in dimensions but where all pixels differ.
	assertDiffs(t, "4029959456464745507", "4029959456464745507-inverted",
		&DiffMetrics{
			NumDiffPixels:     250000,
			PixelDiffPercent:  100.0,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{65535, 65535, 65535, 0},
			DimDiffer:         false})

	// Assert different images where neither fits into the other.
	assertDiffs(t, "fffbcca7e8913ec45b88cc2c6a3a73ad", "fffbcca7e8913ec45b88cc2c6a3a73ad-rotated",
		&DiffMetrics{
			NumDiffPixels:     172466,
			PixelDiffPercent:  74.8550347222,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{65535, 65535, 65535, 0},
			DimDiffer:         true})
	// Make sure the metric is symmetric.
	assertDiffs(t, "fffbcca7e8913ec45b88cc2c6a3a73ad-rotated", "fffbcca7e8913ec45b88cc2c6a3a73ad",
		&DiffMetrics{
			NumDiffPixels:     172466,
			PixelDiffPercent:  74.8550347222,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{65535, 65535, 65535, 0},
			DimDiffer:         true})

	// Compare two images where one has an alpha channel and the other doesn't.
	assertDiffs(t, "b716a12d5b98d04b15db1d9dd82c82ea", "df1591dde35907399734ea19feb76663",
		&DiffMetrics{
			NumDiffPixels:     8750,
			PixelDiffPercent:  2.8483074,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{65535, 514, 65535, 0},
			DimDiffer:         false})

	// Compare two images where the alpha differs.
	assertDiffs(t, "df1591dde35907399734ea19feb76663", "df1591dde35907399734ea19feb76663-6-alpha-diff",
		&DiffMetrics{
			NumDiffPixels:     6,
			PixelDiffPercent:  0.001953125,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{0, 0, 0, 60395},
			DimDiffer:         false})

	// Compare a 16-bit image to itself.
	assertDiffs(t, "aaxfermodes-f16", "aaxfermodes-f16",
		&DiffMetrics{
			NumDiffPixels:     0,
			PixelDiffPercent:  0,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{0, 0, 0, 0},
			DimDiffer:         false})

	// Compare two similar 16-bit images.
	assertDiffs(t, "aaxfermodes-b16", "aaxfermodes-f16",
		&DiffMetrics{
			NumDiffPixels:     217264,
			PixelDiffPercent:  35.32748,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{63662, 57358, 57358, 4351},
			DimDiffer:         false})

	// Compare identical 8-bit and 16-bit images.
	assertDiffs(t, "aaxfermodes-b16", "aaxfermodes-b8",
		&DiffMetrics{
			NumDiffPixels:     0,
			PixelDiffPercent:  0,
			PixelDiffFilePath: "",
			MaxRGBADiffs:      []int{0, 0, 0, 0},
			DimDiffer:         false})
}

const SRC1 = `! SKTEXTSIMPLE
1 5
0x000000ff
0x010000ff
0x000100ff
0x000001ff
0x00000001`

// SRC2 is different in each pixel from SRC1 by 1.
const SRC2 = `! SKTEXTSIMPLE
1 5
0x010000ff
0x020000ff
0x000200ff
0x000002ff
0x00000002`

// SRC3 is different in each pixel from SRC1 by 6.
const SRC3 = `! SKTEXTSIMPLE
1 5
0x060000ff
0x070000ff
0x000700ff
0x000007ff
0x00000007`

// SRC4 is quite different from SRC1.
const SRC4 = `! SKTEXTSIMPLE
1 5
0xffffffff
0xffffffff
0xffffffff
0xffffffff
0xffffffff`

// SRC5 is SRC2 sideways.
const SRC5 = `! SKTEXTSIMPLE
5 1
0x010000ff 0x020000ff 0x000200ff 0x000002ff 0x00000002`

// EXPECTED_1_2 Should have all the pixels as the pixel diff color with an
// offset of 1, except the last pixel which is only different in the alpha by
// an offset of 1.
const EXPECTED_1_2 = `! SKTEXTSIMPLE
1 5
0xfdd0a2ff
0xfdd0a2ff
0xfdd0a2ff
0xfdd0a2ff
0xc6dbefff`

// EXPECTED_1_3 Should have all the pixels as the pixel diff color with an
// offset of 6, except the last pixel which is only different in the alpha by
// an offet of 6.
const EXPECTED_1_3 = `! SKTEXTSIMPLE
1 5
0xfd8d3cff
0xfd8d3cff
0xfd8d3cff
0xfd8d3cff
0x6baed6ff`

// EXPECTED_1_4 Should have all the pixels as the pixel diff color with an
// offset of 6, except the last pixel which is only different in the alpha by
// an offet of 6.
const EXPECTED_1_4 = `! SKTEXTSIMPLE
1 5
0x7f2704ff
0x7f2704ff
0x7f2704ff
0x7f2704ff
0x7f2704ff`

// EXPECTED_NO_DIFF should be all black transparent since there are no differences.
const EXPECTED_NO_DIFF = `! SKTEXTSIMPLE
1 5
0x00000000
0x00000000
0x00000000
0x00000000
0x00000000`

const EXPECTED_2_5 = `! SKTEXTSIMPLE
5 5
0x00000000 0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff
0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff
0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff
0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff
0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff 0x7f2704ff`

// imageFromString decodes the SKTEXT image from the string.
func imageFromString(t *testing.T, s string) *image.NRGBA {
	buf := bytes.NewBufferString(s)
	img, err := text.Decode(buf)
	if err != nil {
		t.Fatalf("Failed to decode a valid image: %s", err)
	}
	return img.(*image.NRGBA)
}

// lineDiff lists the differences in the lines of a and b.
func lineDiff(t *testing.T, a, b string) {
	aslice := strings.Split(a, "\n")
	bslice := strings.Split(b, "\n")
	if len(aslice) != len(bslice) {
		t.Fatal("Can't diff text, mismatched number of lines.\n")
		return
	}
	for i, s := range aslice {
		if s != bslice[i] {
			t.Errorf("Line %d: %q != %q\n", i+1, s, bslice[i])
		}
	}
}

// assertImagesEqual asserts that the two images are identical.
func assertImagesEqual(t *testing.T, got, want *image.NRGBA) {
	// Do the compare by converting them to sktext format and doing a string
	// compare.
	gotbuf := &bytes.Buffer{}
	if err := text.Encode(gotbuf, got); err != nil {
		t.Fatalf("Failed to encode: %s", err)
	}
	wantbuf := &bytes.Buffer{}
	if err := text.Encode(wantbuf, want); err != nil {
		t.Fatalf("Failed to encode: %s", err)
	}
	if gotbuf.String() != wantbuf.String() {
		t.Errorf("Pixel mismatch:\nGot:\n\n%v\n\nWant:\n\n%v\n", gotbuf, wantbuf)
		/// Also print out the lines that are different, to make debugging easier.
		lineDiff(t, gotbuf.String(), wantbuf.String())
	}
}

// assertDiffMatch asserts that you get expected when you diff
// src1 and src2.
//
// Note that all images are in sktext format as strings.
func assertDiffMatch(t *testing.T, expected, src1, src2 string, expectedDiffMetrics ...*DiffMetrics) {
	dm, got := Diff(imageFromString(t, src1), imageFromString(t, src2))
	want := imageFromString(t, expected)
	assertImagesEqual(t, got, want)

	for _, expDM := range expectedDiffMetrics {
		assert.Equal(t, expDM, dm)
	}
}

// TestDiffImages tests that the diff images produced are correct.
func TestDiffImages(t *testing.T) {
	assertDiffMatch(t, EXPECTED_NO_DIFF, SRC1, SRC1)
	assertDiffMatch(t, EXPECTED_NO_DIFF, SRC2, SRC2)
	assertDiffMatch(t, EXPECTED_1_2, SRC1, SRC2)
	assertDiffMatch(t, EXPECTED_1_2, SRC2, SRC1)
	assertDiffMatch(t, EXPECTED_1_3, SRC3, SRC1)
	assertDiffMatch(t, EXPECTED_1_3, SRC1, SRC3)
	assertDiffMatch(t, EXPECTED_1_4, SRC1, SRC4)
	assertDiffMatch(t, EXPECTED_1_4, SRC4, SRC1)
	assertDiffMatch(t, EXPECTED_2_5, SRC2, SRC5, &DiffMetrics{
		NumDiffPixels:     24,
		PixelDiffPercent:  (24.0 / 25.0) * 100,
		PixelDiffFilePath: "",
		MaxRGBADiffs:      []int{65535, 65535, 65535, 0},
		DimDiffer:         true,
	})
}

// assertDiffs asserts that the DiffMetrics reported by Diffing the two images
// matches the expected DiffMetrics.
func assertDiffs(t *testing.T, d1, d2 string, expectedDiffMetrics *DiffMetrics) {
	img1, err := OpenImage(filepath.Join(TESTDATA_DIR, d1+".png"))
	if err != nil {
		t.Fatal("Failed to open test file: ", err)
	}
	img2, err := OpenImage(filepath.Join(TESTDATA_DIR, d2+".png"))
	if err != nil {
		t.Fatal("Failed to open test file: ", err)
	}

	diffMetrics, _ := Diff(img1, img2)
	if err != nil {
		t.Error("Unexpected error: ", err)
	}
	if got, want := diffMetrics, expectedDiffMetrics; !reflect.DeepEqual(got, want) {
		t.Errorf("Image Diff: Got %v Want %v", got, want)
	}
}

func TestDeltaOffset(t *testing.T) {
	testCases := []struct {
		offset int
		want   int
	}{
		{
			offset: 257,
			want:   0,
		},
		{
			offset: 514,
			want:   1,
		},
		{
			offset: 1285,
			want:   1,
		},
		{
			offset: 1542,
			want:   2,
		},
		{
			offset: 25700,
			want:   4,
		},
		{
			offset: 262140,
			want:   6,
		},
	}

	for _, tc := range testCases {
		if got, want := deltaOffset(tc.offset), tc.want; got != want {
			t.Errorf("deltaOffset(%d): Got %v Want %v", tc.offset, got, want)
		}
	}

}

var (
	img1            image.Image
	img2            image.Image
	aaxfermodes_b8  image.Image
	aaxfermodes_f16 image.Image
	aaxfermodes_b16 image.Image
	once            sync.Once
)

func loadBenchmarkImages() {
	var err error
	img1, err = OpenImage(filepath.Join(TESTDATA_DIR, "4029959456464745507.png"))
	if err != nil {
		glog.Fatal("Failed to open test file: ", err)
	}
	img2, err = OpenImage(filepath.Join(TESTDATA_DIR, "16465366847175223174.png"))
	if err != nil {
		glog.Fatal("Failed to open test file: ", err)
	}
	aaxfermodes_b8, err = OpenImage(filepath.Join(TESTDATA_DIR, "aaxfermodes-b8.png"))
	if err != nil {
		glog.Fatal("Failed to open test file: ", err)
	}
	aaxfermodes_f16, err = OpenImage(filepath.Join(TESTDATA_DIR, "aaxfermodes-f16.png"))
	if err != nil {
		glog.Fatal("Failed to open test file: ", err)
	}
	aaxfermodes_b16, err = OpenImage(filepath.Join(TESTDATA_DIR, "aaxfermodes-b16.png"))
	if err != nil {
		glog.Fatal("Failed to open test file: ", err)
	}
}

func BenchmarkDiff_32_32_identical(b *testing.B) {
	once.Do(loadBenchmarkImages)
	for i := 0; i < b.N; i++ {
		Diff(img1, img1)
	}
}

func BenchmarkDiff_32_32_similar(b *testing.B) {
	once.Do(loadBenchmarkImages)
	for i := 0; i < b.N; i++ {
		Diff(img1, img2)
	}
}

func BenchmarkDiff_64_64_identical(b *testing.B) {
	once.Do(loadBenchmarkImages)
	for i := 0; i < b.N; i++ {
		Diff(aaxfermodes_b16, aaxfermodes_b16)
	}
}

func BenchmarkDiff_64_64_similar(b *testing.B) {
	once.Do(loadBenchmarkImages)
	for i := 0; i < b.N; i++ {
		Diff(aaxfermodes_b16, aaxfermodes_f16)
	}
}

func BenchmarkDiff_32_64_identical(b *testing.B) {
	once.Do(loadBenchmarkImages)
	for i := 0; i < b.N; i++ {
		Diff(aaxfermodes_b8, aaxfermodes_b16)
	}
}

func BenchmarkDiff_32_64_similar(b *testing.B) {
	once.Do(loadBenchmarkImages)
	for i := 0; i < b.N; i++ {
		Diff(aaxfermodes_b8, aaxfermodes_f16)
	}
}
