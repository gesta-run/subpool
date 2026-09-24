package id

import "testing"

func TestValid(t *testing.T) {
	for _, value := range []string{
		"00000000-0000-4000-8000-000000000001",
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF",
	} {
		if !Valid(value) {
			t.Fatalf("Valid(%q) = false", value)
		}
	}
	for _, value := range []string{"", "not-a-uuid", "00000000-0000-4000-8000-00000000000z", "000000000000-4000-8000-000000000001"} {
		if Valid(value) {
			t.Fatalf("Valid(%q) = true", value)
		}
	}
}
