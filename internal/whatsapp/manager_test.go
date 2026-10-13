package whatsapp

import "testing"

func TestNumberAllowed(t *testing.T) {
	cases := []struct {
		entry, sender string
		want          bool
	}{
		{"41790000000", "41790000000", true},
		{"+41 79 000 00 00", "41790000000", true},
		{"0041790000000", "41790000000", true},
		{"0790000000", "41790000000", true},
		// An international entry never matches as a suffix.
		{"41790000000", "141790000000", false},
		{"+790000000", "41790000000", false},
		{"9", "41790000000", false},
		{"09", "41790000000", false},
		{"", "41790000000", false},
		{"0790000000", "790000001", false},
	}
	for _, c := range cases {
		if got := numberAllowed(c.entry, c.sender); got != c.want {
			t.Errorf("numberAllowed(%q, %q) = %v, want %v", c.entry, c.sender, got, c.want)
		}
	}
}
