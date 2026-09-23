package commands

import "testing"

func TestIsRelease(t *testing.T) {
	tests := map[string]bool{
		"v1.0.0":         true,
		"v1.12.3":        true,
		"v2.0.0-alpha.1": true,
		"v2.0.0-rc1":     true,

		"dev":                                  false,
		"effaf67":                              false,
		"effaf67-dirty":                        false,
		"v1.0.0-dirty":                         false,
		"v1.0.0-3-gabc1234":                    false,
		"v1.0.0-3-gabc1234-dirty":              false,
		"v0.0.0-20260923185155-effaf67b0d2c":   false,
		"v1.0.1-0.20260923185155-effaf67b0d2c": false,
		"v0.0.0-20260923185155-effaf67b0d2c+dirty": false,
		"1.0.0": false,
		"v1.0":  false,
	}
	for v, want := range tests {
		if got := isRelease(v); got != want {
			t.Errorf("isRelease(%q) = %v, want %v", v, got, want)
		}
	}
}
