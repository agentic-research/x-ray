package cartographer

import (
	"math"
	"testing"
)

func TestQuantizeD8_Zero(t *testing.T) {
	x := [8]float64{}
	y := QuantizeD8(x)
	if y != x {
		t.Errorf("Zero should quantize to zero, got %v", y)
	}
	if !IsEvenSum(y) {
		t.Error("D8 output should have even sum")
	}
}

func TestQuantizeD8_EvenParity(t *testing.T) {
	x := [8]float64{1.1, 0.9, 0.1, -0.1, 0.0, 0.0, 0.0, 0.0}
	y := QuantizeD8(x)
	if !IsEvenSum(y) {
		t.Errorf("D8 output should have even sum, got %v (sum=%v)", y, e8SumOf(y))
	}
}

func TestQuantizeD8_OddParityFixed(t *testing.T) {
	x := [8]float64{0.9, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1, 0.1}
	y := QuantizeD8(x)
	if !IsEvenSum(y) {
		t.Errorf("D8 output should have even sum, got %v", y)
	}
}

func TestQuantizeE8_Zero(t *testing.T) {
	x := [8]float64{}
	y := QuantizeE8(x)
	if y != x {
		t.Errorf("Zero should quantize to zero, got %v", y)
	}
}

func TestQuantizeE8_HalfIntegerCoset(t *testing.T) {
	x := [8]float64{0.6, 0.4, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5}
	y := QuantizeE8(x)
	dist := math.Sqrt(distSq8(x, y))
	if dist > E8CoveringRadius+0.01 {
		t.Errorf("E8 distance %f exceeds covering radius %f for near-half-integer point", dist, E8CoveringRadius)
	}
}

func TestQuantizeE8_IntegerCoset(t *testing.T) {
	x := [8]float64{2.1, 0.05, -0.05, 0.05, -0.05, 0.05, -0.05, 0.0}
	y := QuantizeE8(x)
	dist := math.Sqrt(distSq8(x, y))
	if dist > E8CoveringRadius+0.01 {
		t.Errorf("E8 distance %f exceeds covering radius %f for near-integer point", dist, E8CoveringRadius)
	}
}

func TestQuantizeE8_Determinism(t *testing.T) {
	x := [8]float64{1.3, -0.7, 2.1, 0.4, -1.8, 0.9, 0.0, 3.3}
	y1 := QuantizeE8(x)
	y2 := QuantizeE8(x)
	if y1 != y2 {
		t.Errorf("E8 should be deterministic: %v != %v", y1, y2)
	}
}

func TestE8Distance(t *testing.T) {
	x := [8]float64{}
	if d := E8Distance(x); d != 0 {
		t.Errorf("Distance from zero to E8 should be 0, got %f", d)
	}
}

func e8SumOf(x [8]float64) float64 {
	var s float64
	for _, v := range x {
		s += v
	}
	return s
}
