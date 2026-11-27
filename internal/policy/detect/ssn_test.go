package detect

import "testing"

func TestSSNFindsValidNumbersOnly(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"one number in a sentence", "my SSN is 123-45-6789", []string{"123-45-6789"}},
		{"two numbers", "a 123-45-6789 and 234-56-7890", []string{"123-45-6789", "234-56-7890"}},
		{"area 000 is not issued", "000-12-3456", nil},
		{"area 666 is not issued", "666-12-3456", nil},
		{"area 900 and up is not issued", "900-12-3456", nil},
		{"area 999 is not issued", "999-12-3456", nil},
		{"group 00 is not issued", "123-00-4567", nil},
		{"serial 0000 is not issued", "123-45-0000", nil},
		{"a phone number is not an SSN", "555-123-4567", nil},
		{"four digit first group", "1234-56-7890", nil},
		{"no dashes", "123456789", nil},
		{"letters touching the number", "x123-45-6789y", nil},
		{"empty text", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SSN().Find(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("Find(%q) returned %d findings, want %d: %+v", tt.text, len(got), len(tt.want), got)
			}
			for i, f := range got {
				if f.Kind != "ssn" {
					t.Errorf("finding %d kind = %q, want ssn", i, f.Kind)
				}
				if f.Confidence != 1 {
					t.Errorf("finding %d confidence = %v, want 1", i, f.Confidence)
				}
				if s := tt.text[f.Start:f.End]; s != tt.want[i] {
					t.Errorf("finding %d covers %q, want %q", i, s, tt.want[i])
				}
			}
		})
	}
}
