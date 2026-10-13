package claudemetrics

import "testing"

func f(v float64) *float64 { return &v }

func TestAggregateSumsAcrossAccounts(t *testing.T) {
	// Two accounts, three buckets. tok_in: account A [10, nil, 5], B [1, 2, nil].
	a := History{
		From: 100, To: 130, Step: 10,
		Series: map[string][]Point{
			"tok_in":      {{T: 110, V: f(10)}, {T: 120, V: nil}, {T: 130, V: f(5)}},
			"sess_active": {{T: 110, V: f(2)}, {T: 120, V: f(1)}, {T: 130, V: nil}},
		},
	}
	b := History{
		From: 100, To: 130, Step: 10,
		Series: map[string][]Point{
			"tok_in":      {{T: 110, V: f(1)}, {T: 120, V: f(2)}, {T: 130, V: nil}},
			"sess_active": {{T: 110, V: f(3)}, {T: 120, V: nil}, {T: 130, V: nil}},
		},
	}
	got := Aggregate("hour", []History{a, b})

	want := []struct {
		ds  string
		idx int
		v   *float64
	}{
		{"tok_in", 0, f(11)},      // 10 + 1
		{"tok_in", 1, f(2)},       // nil + 2
		{"tok_in", 2, f(5)},       // 5 + nil
		{"sess_active", 0, f(5)},  // 2 + 3
		{"sess_active", 1, f(1)},  // 1 + nil
		{"sess_active", 2, nil},   // nil + nil => nil
	}
	for _, w := range want {
		p := got.Series[w.ds][w.idx]
		if (p.V == nil) != (w.v == nil) {
			t.Fatalf("%s[%d] nil mismatch: got %v want %v", w.ds, w.idx, p.V, w.v)
		}
		if p.V != nil && *p.V != *w.v {
			t.Fatalf("%s[%d] = %v; want %v", w.ds, w.idx, *p.V, *w.v)
		}
	}
	if got.From != 100 || got.Step != 10 {
		t.Fatalf("meta not carried: from=%d step=%d", got.From, got.Step)
	}
}
