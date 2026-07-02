package main

import "testing"

func TestFormatNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   float64
		want string
	}{
		{name: "int", in: 60, want: "60"},
		{name: "fraction", in: 2.5, want: "2.5"},
		{name: "trim zeros", in: 3.125000, want: "3.125"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatNumber(tt.in); got != tt.want {
				t.Fatalf("formatNumber(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
