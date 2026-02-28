package cartographer

import (
	"math"
	"math/cmplx"
)

// FFTFeatures holds frequency-domain features extracted from a grayscale
// image region. These describe the repeating visual patterns (grids, lists,
// table rows) within the region — structure that exists in the pixel data
// even when no DOM is available (canvas, WebGL, native apps).
type FFTFeatures struct {
	DominantFreqX float64 // strongest horizontal repetition frequency (0 = none)
	DominantFreqY float64 // strongest vertical repetition frequency
	PeakStrength  float64 // magnitude of strongest peak relative to DC [0,1]
	GridScore     float64 // how grid-like the region is [0,1]
	Entropy       float64 // spectral entropy [0,1] — high = complex, low = regular
}

// AnalyzeRegion computes FFT features for a grayscale image region.
// gray is a row-major slice of pixel intensities [0,255], w×h pixels.
// Returns zero features if the region is too small for meaningful analysis.
func AnalyzeRegion(gray []float64, w, h int) FFTFeatures {
	if w < 8 || h < 8 || len(gray) < w*h {
		return FFTFeatures{}
	}

	// Pad to next power of 2 for efficient FFT.
	pw := nextPow2(w)
	ph := nextPow2(h)

	// Build complex matrix with Hann windowing to reduce spectral leakage.
	data := make([]complex128, pw*ph)
	for y := 0; y < h; y++ {
		wy := hannWindow(y, h)
		for x := 0; x < w; x++ {
			wx := hannWindow(x, w)
			data[y*pw+x] = complex(gray[y*w+x]*wx*wy, 0)
		}
	}

	// 2D FFT via row-column decomposition.
	fft2D(data, pw, ph)

	// Compute power spectrum (skip DC at (0,0)).
	power := make([]float64, pw*ph)
	var maxPower float64
	dcPower := cmplx.Abs(data[0]) * cmplx.Abs(data[0])
	for i := 1; i < pw*ph; i++ {
		p := cmplx.Abs(data[i])
		power[i] = p * p
		if power[i] > maxPower {
			maxPower = power[i]
		}
	}

	if dcPower == 0 || maxPower == 0 {
		return FFTFeatures{}
	}

	// Find dominant peaks: local maxima above mean + 3σ.
	var sum, sumSq float64
	n := float64(pw*ph - 1)
	for i := 1; i < pw*ph; i++ {
		sum += power[i]
		sumSq += power[i] * power[i]
	}
	mean := sum / n
	variance := sumSq/n - mean*mean
	if variance < 0 {
		variance = 0
	}
	threshold := mean + 3*math.Sqrt(variance)

	// Find peaks along horizontal (row 0, varying x) and vertical (col 0, varying y) axes.
	var bestHFreq, bestVFreq float64
	var bestHPow, bestVPow float64

	// Horizontal frequencies: row 0, columns 2..pw/2
	// Skip bin 1: a single cycle across the region isn't a repeating pattern
	// (also absorbs Hann window edge artifacts).
	for fx := 2; fx <= pw/2; fx++ {
		p := power[fx]
		if p > threshold && p > bestHPow {
			bestHPow = p
			bestHFreq = float64(fx)
		}
	}

	// Vertical frequencies: column 0, rows 2..ph/2
	for fy := 2; fy <= ph/2; fy++ {
		p := power[fy*pw]
		if p > threshold && p > bestVPow {
			bestVPow = p
			bestVFreq = float64(fy)
		}
	}

	// Peak strength: relative to DC component.
	peakStrength := math.Min(1.0, maxPower/dcPower)

	// Grid score: presence of both horizontal and vertical dominant frequencies.
	var gridScore float64
	if bestHFreq > 0 && bestVFreq > 0 {
		// Both axes have structure — strong grid signal.
		gridScore = math.Min(1.0, (bestHPow+bestVPow)/(2*maxPower))
		if gridScore < 0.3 {
			gridScore = 0.3 // floor: if both present, at least 0.3
		}
	} else if bestHFreq > 0 || bestVFreq > 0 {
		// Single axis: list or column layout.
		gridScore = 0.1
	}

	// Spectral entropy: normalized Shannon entropy of the power spectrum.
	// Low entropy = regular repeating pattern, high = noisy/complex.
	entropy := spectralEntropy(power, sum)

	return FFTFeatures{
		DominantFreqX: bestHFreq,
		DominantFreqY: bestVFreq,
		PeakStrength:  peakStrength,
		GridScore:     gridScore,
		Entropy:       entropy,
	}
}

// spectralEntropy computes normalized Shannon entropy of the power spectrum.
func spectralEntropy(power []float64, totalPower float64) float64 {
	if totalPower == 0 {
		return 0
	}
	var entropy float64
	maxEntropy := math.Log2(float64(len(power) - 1))
	if maxEntropy == 0 {
		return 0
	}
	for i := 1; i < len(power); i++ {
		if power[i] > 0 {
			p := power[i] / totalPower
			entropy -= p * math.Log2(p)
		}
	}
	return math.Min(1.0, entropy/maxEntropy)
}

// hannWindow returns the Hann window coefficient for position i in a window of size n.
func hannWindow(i, n int) float64 {
	if n <= 1 {
		return 1
	}
	return 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n-1)))
}

// nextPow2 returns the smallest power of 2 >= n.
func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// ---------------------------------------------------------------------------
// Cooley-Tukey radix-2 DIT FFT
// ---------------------------------------------------------------------------

// fft1D computes the in-place DFT of x (must be power-of-2 length).
func fft1D(x []complex128) {
	n := len(x)
	if n <= 1 {
		return
	}

	// Bit-reversal permutation.
	j := 0
	for i := 1; i < n; i++ {
		bit := n >> 1
		for j&bit != 0 {
			j ^= bit
			bit >>= 1
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}

	// Butterfly stages.
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		wn := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			w := complex(1, 0)
			for k := 0; k < half; k++ {
				u := x[start+k]
				t := w * x[start+k+half]
				x[start+k] = u + t
				x[start+k+half] = u - t
				w *= wn
			}
		}
	}
}

// fft2D computes the 2D FFT via row-column decomposition.
// data is row-major, pw columns × ph rows (both must be powers of 2).
func fft2D(data []complex128, pw, ph int) {
	// FFT each row.
	row := make([]complex128, pw)
	for y := 0; y < ph; y++ {
		copy(row, data[y*pw:(y+1)*pw])
		fft1D(row)
		copy(data[y*pw:(y+1)*pw], row)
	}

	// FFT each column.
	col := make([]complex128, ph)
	for x := 0; x < pw; x++ {
		for y := 0; y < ph; y++ {
			col[y] = data[y*pw+x]
		}
		fft1D(col)
		for y := 0; y < ph; y++ {
			data[y*pw+x] = col[y]
		}
	}
}
