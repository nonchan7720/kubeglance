package domain

import "testing"

func TestSumByInstant(t *testing.T) {
	a := []Sample{{At: at(0), Usage: Resource{CPU: 1}}, {At: at(15), Usage: Resource{CPU: 2}}, {At: at(30), Usage: Resource{CPU: 3}}}
	b := []Sample{{At: at(15), Usage: Resource{CPU: 10}}, {At: at(30), Usage: Resource{CPU: 20}}, {At: at(45), Usage: Resource{CPU: 30}}}
	got := SumByInstant(a, b)
	want := []int64{1, 12, 23, 30}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i, w := range want {
		if got[i].Usage.CPU != w {
			t.Errorf("[%d] = %d, want %d", i, got[i].Usage.CPU, w)
		}
		if i > 0 && !got[i].At.After(got[i-1].At) {
			t.Errorf("not ascending at %d", i)
		}
	}
}

func TestSumByInstantEmpty(t *testing.T) {
	if got := SumByInstant(); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	if got := SumByInstant(nil, []Sample{}); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestImbalance(t *testing.T) {
	// ratios 0.2, 0.4, 0.9 → spread 0.7, mean 0.5, stddev ≈ 0.294
	im := Imbalance([]float64{0.2, 0.4, 0.9})
	if im.Spread < 0.699 || im.Spread > 0.701 {
		t.Errorf("spread = %v, want 0.7", im.Spread)
	}
	if im.StdDev < 0.29 || im.StdDev > 0.30 {
		t.Errorf("stddev = %v, want ~0.294", im.StdDev)
	}
	if im.Max != 0.9 || im.Min != 0.2 {
		t.Errorf("min/max = %v/%v", im.Min, im.Max)
	}
	if Imbalance(nil) != (ImbalanceStats{}) {
		t.Error("empty imbalance not zero")
	}
	if Imbalance([]float64{0.5}).Spread != 0 {
		t.Error("single node has spread")
	}
}
