package cartographer

import (
	"math"
	"testing"
)

func TestFFT1D_Impulse(t *testing.T) {
	// A single impulse at position 0 should produce flat magnitude spectrum.
	x := make([]complex128, 8)
	x[0] = 1
	fft1D(x)

	for i, v := range x {
		mag := real(v)*real(v) + imag(v)*imag(v)
		if math.Abs(mag-1.0) > 1e-10 {
			t.Errorf("FFT[%d] magnitude = %f, want 1.0", i, mag)
		}
	}
}

func TestFFT1D_DC(t *testing.T) {
	// Constant signal: all energy at DC (bin 0).
	x := make([]complex128, 8)
	for i := range x {
		x[i] = 3
	}
	fft1D(x)

	if real(x[0]) != 24 { // N * value = 8 * 3
		t.Errorf("DC bin = %f, want 24", real(x[0]))
	}
	for i := 1; i < len(x); i++ {
		mag := math.Sqrt(real(x[i])*real(x[i]) + imag(x[i])*imag(x[i]))
		if mag > 1e-10 {
			t.Errorf("FFT[%d] should be zero, got magnitude %f", i, mag)
		}
	}
}

func TestFFT1D_Sine(t *testing.T) {
	// Pure sine at frequency 2 (2 cycles in 16 samples).
	n := 16
	x := make([]complex128, n)
	for i := range x {
		x[i] = complex(math.Sin(2*math.Pi*2*float64(i)/float64(n)), 0)
	}
	fft1D(x)

	// Peaks should be at bins 2 and N-2.
	for i := 0; i < n; i++ {
		mag := math.Sqrt(real(x[i])*real(x[i]) + imag(x[i])*imag(x[i]))
		if i == 2 || i == n-2 {
			if mag < 1.0 {
				t.Errorf("FFT[%d] magnitude = %f, want large", i, mag)
			}
		} else {
			if mag > 1e-6 {
				t.Errorf("FFT[%d] magnitude = %f, want ~0", i, mag)
			}
		}
	}
}

func TestNextPow2(t *testing.T) {
	cases := []struct{ in, want int }{
		{1, 1}, {2, 2}, {3, 4}, {5, 8}, {16, 16}, {17, 32}, {100, 128},
	}
	for _, tc := range cases {
		got := nextPow2(tc.in)
		if got != tc.want {
			t.Errorf("nextPow2(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestHannWindow(t *testing.T) {
	// Endpoints should be 0, center should be 1.
	if v := hannWindow(0, 16); v > 1e-10 {
		t.Errorf("hann(0,16) = %f, want ~0", v)
	}
	if v := hannWindow(8, 17); math.Abs(v-1.0) > 1e-10 {
		t.Errorf("hann(8,17) = %f, want ~1", v)
	}
}

func TestAnalyzeRegion_HorizontalStripes(t *testing.T) {
	// Create a 64x64 image with horizontal stripes every 8 pixels.
	// This should produce a strong vertical frequency peak at f_y = 8 (64/8 = 8 cycles).
	w, h := 64, 64
	gray := make([]float64, w*h)
	for y := 0; y < h; y++ {
		val := 255.0
		if (y/8)%2 == 0 {
			val = 0
		}
		for x := 0; x < w; x++ {
			gray[y*w+x] = val
		}
	}

	feat := AnalyzeRegion(gray, w, h)

	if feat.DominantFreqY == 0 {
		t.Error("expected dominant vertical frequency for horizontal stripes, got 0")
	}
	if feat.PeakStrength < 0.01 {
		t.Errorf("peak strength too low: %f", feat.PeakStrength)
	}
	t.Logf("Horizontal stripes: freqX=%.0f freqY=%.0f peak=%.4f grid=%.4f entropy=%.4f",
		feat.DominantFreqX, feat.DominantFreqY, feat.PeakStrength, feat.GridScore, feat.Entropy)
}

func TestAnalyzeRegion_VerticalStripes(t *testing.T) {
	// Vertical stripes every 16 pixels.
	w, h := 64, 64
	gray := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/16)%2 == 0 {
				gray[y*w+x] = 255
			}
		}
	}

	feat := AnalyzeRegion(gray, w, h)

	if feat.DominantFreqX == 0 {
		t.Error("expected dominant horizontal frequency for vertical stripes, got 0")
	}
	t.Logf("Vertical stripes: freqX=%.0f freqY=%.0f peak=%.4f grid=%.4f entropy=%.4f",
		feat.DominantFreqX, feat.DominantFreqY, feat.PeakStrength, feat.GridScore, feat.Entropy)
}

func TestAnalyzeRegion_Grid(t *testing.T) {
	// Grid pattern: both horizontal and vertical stripes.
	w, h := 64, 64
	gray := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			hStripe := (y/8)%2 == 0
			vStripe := (x/8)%2 == 0
			if hStripe || vStripe {
				gray[y*w+x] = 255
			}
		}
	}

	feat := AnalyzeRegion(gray, w, h)

	if feat.DominantFreqX == 0 || feat.DominantFreqY == 0 {
		t.Errorf("expected both frequencies for grid, got freqX=%.0f freqY=%.0f",
			feat.DominantFreqX, feat.DominantFreqY)
	}
	if feat.GridScore < 0.3 {
		t.Errorf("grid score too low: %f, expected >= 0.3", feat.GridScore)
	}
	t.Logf("Grid: freqX=%.0f freqY=%.0f peak=%.4f grid=%.4f entropy=%.4f",
		feat.DominantFreqX, feat.DominantFreqY, feat.PeakStrength, feat.GridScore, feat.Entropy)
}

func TestAnalyzeRegion_Uniform(t *testing.T) {
	// Uniform gray image — no peaks expected.
	w, h := 32, 32
	gray := make([]float64, w*h)
	for i := range gray {
		gray[i] = 128
	}

	feat := AnalyzeRegion(gray, w, h)

	if feat.DominantFreqX != 0 || feat.DominantFreqY != 0 {
		t.Errorf("expected no dominant frequencies for uniform image, got freqX=%.0f freqY=%.0f",
			feat.DominantFreqX, feat.DominantFreqY)
	}
	t.Logf("Uniform: freqX=%.0f freqY=%.0f peak=%.4f grid=%.4f entropy=%.4f",
		feat.DominantFreqX, feat.DominantFreqY, feat.PeakStrength, feat.GridScore, feat.Entropy)
}

func TestAnalyzeRegion_TooSmall(t *testing.T) {
	// Regions smaller than 8x8 should return zero features.
	feat := AnalyzeRegion(make([]float64, 16), 4, 4)
	if feat.DominantFreqX != 0 || feat.DominantFreqY != 0 || feat.PeakStrength != 0 {
		t.Error("expected zero features for tiny region")
	}
}

func TestAnalyzeRegion_ListRows(t *testing.T) {
	// Simulate a list UI: alternating light/dark rows every 20 pixels
	// in a 100x200 region (10 rows). This is what a Reddit feed looks like
	// when rendered to canvas.
	w, h := 100, 200
	gray := make([]float64, w*h)
	for y := 0; y < h; y++ {
		val := 240.0 // light row
		if (y/20)%2 == 1 {
			val = 200.0 // slightly darker alternating row
		}
		// Add a 1px border between rows
		if y%20 == 0 {
			val = 100.0
		}
		for x := 0; x < w; x++ {
			gray[y*w+x] = val
		}
	}

	feat := AnalyzeRegion(gray, w, h)

	if feat.DominantFreqY == 0 {
		t.Error("expected vertical frequency for list rows")
	}
	// The entropy should be relatively low (regular repeating pattern).
	if feat.Entropy > 0.5 {
		t.Errorf("entropy too high for regular list: %f", feat.Entropy)
	}
	t.Logf("List rows: freqX=%.0f freqY=%.0f peak=%.4f grid=%.4f entropy=%.4f",
		feat.DominantFreqX, feat.DominantFreqY, feat.PeakStrength, feat.GridScore, feat.Entropy)
}

func BenchmarkFFT1D_1024(b *testing.B) {
	x := make([]complex128, 1024)
	for i := range x {
		x[i] = complex(float64(i%256), 0)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tmp := make([]complex128, 1024)
		copy(tmp, x)
		fft1D(tmp)
	}
}

func BenchmarkAnalyzeRegion_300x400(b *testing.B) {
	w, h := 300, 400
	gray := make([]float64, w*h)
	for i := range gray {
		gray[i] = float64(i % 256)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AnalyzeRegion(gray, w, h)
	}
}
