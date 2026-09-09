package main

import (
	"strings"
	"testing"
)

func TestPrintSkills(t *testing.T) {
	tests := []struct {
		name   string
		skills map[string]string
		want   string
	}{
		{
			name:   "none discovered",
			skills: map[string]string{},
			want:   "  skills  0 loaded\n",
		},
		{
			name:   "sorted regardless of map order",
			skills: map[string]string{"zeta": "z", "alpha": "a", "mid": "m"},
			want:   "  skills  3 loaded\n    - alpha\n    - mid\n    - zeta\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			printSkills(&b, tt.skills)
			if got := b.String(); got != tt.want {
				t.Errorf("printSkills() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
