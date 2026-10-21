package entity

import "testing"

func TestIsWellFormedTaxId(t *testing.T) {
	tests := []struct {
		taxId string
		want  bool
	}{
		{"BE1034615262", true},
		{"be 1034-615.262", true},
		{"1234563218", true},
		{"\u0412E1034615262", false}, // Cyrillic В
		{"BE1034\u00a0615262", false},
		{"BE/1034615262", false},
		{"", false},
		{" - ", false},
	}
	for _, tt := range tests {
		if got := IsWellFormedTaxId(tt.taxId); got != tt.want {
			t.Errorf("IsWellFormedTaxId(%q) = %t, want %t", tt.taxId, got, tt.want)
		}
	}
}
