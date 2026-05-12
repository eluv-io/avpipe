package main

import (
	"math"
	"testing"
)

func TestParseFPSDecimal(t *testing.T) {
	fps, err := parseFPS("23.976")
	if err != nil {
		t.Fatal(err)
	}
	if fps != 23.976 {
		t.Fatalf("fps = %v, want 23.976", fps)
	}
}

func TestParseFPSRatio(t *testing.T) {
	fps, err := parseFPS("24000/1001")
	if err != nil {
		t.Fatal(err)
	}
	want := float64(24000) / float64(1001)
	if math.Abs(fps-want) > 0.000001 {
		t.Fatalf("fps = %v, want %v", fps, want)
	}
}

func TestParseFPSRejectsZeroDenominator(t *testing.T) {
	if _, err := parseFPS("24000/0"); err == nil {
		t.Fatal("expected error")
	}
}
