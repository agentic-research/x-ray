package cartographer

import (
	"math"
	"testing"
)

func TestDecodeGolay24(t *testing.T) {
	var zeros [24]byte
	output := DecodeGolay24(zeros)
	if output != zeros {
		t.Errorf("Expected zeros to decode to zeros, got %v", output)
	}

	// Introduce 3 errors (Golay can correct up to 3)
	noisyZeros := zeros
	noisyZeros[0] = 1
	noisyZeros[12] = 1
	noisyZeros[23] = 1

	output = DecodeGolay24(noisyZeros)
	if output != zeros {
		t.Errorf("Expected 3 errors to be corrected to zeros, got %v", output)
	}
}

func TestDecodeGolay24_AllOnes(t *testing.T) {
	var ones [24]byte
	for i := range ones {
		ones[i] = 1
	}
	output := DecodeGolay24(ones)
	if output != ones {
		t.Errorf("Expected all-ones to be a valid codeword, got %v", output)
	}
}

func TestDecodeGolay24_SingleError(t *testing.T) {
	var zeros [24]byte
	for pos := 0; pos < 24; pos++ {
		noisy := zeros
		noisy[pos] = 1
		output := DecodeGolay24(noisy)
		if output != zeros {
			t.Errorf("Failed to correct single error at position %d", pos)
		}
	}
}

func TestAdelicPhaseQuantizeInt16(t *testing.T) {
	input := [6]int16{0, 0, 0, 0, 0, 0}
	output := AdelicPhaseQuantizeInt16(input)
	for i, val := range output {
		if val > 5 {
			t.Errorf("Expected root index <= 5, got %d at %d", val, i)
		}
	}
}

func TestAdelicPhaseQuantizeFloat(t *testing.T) {
	pi3 := math.Pi / 3.0
	input := [6]float64{0.0, pi3, 2 * pi3, 3 * pi3, 4 * pi3, 5 * pi3}
	output := AdelicPhaseQuantizeFloat(input)
	for i, val := range output {
		if val < 0 || val > math.Pi*2 {
			t.Errorf("Expected phase between 0 and 2PI, got %f at %d", val, i)
		}
	}
}
