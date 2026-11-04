package people

import "testing"

func TestDirectoryKeepsIdentitiesAndResolvesDisplayNames(t *testing.T) {
	d, err := Parse(`{"names":{"AM":"Alex Morgan","alex@example.test":"Alex Morgan","jordan@example.test":"Jordan Lee"},"excluded":[" retired@example.test "]}`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name("am") != "Alex Morgan" || d.Name("local alias", "ALEX@example.test") != "Alex Morgan" || d.Name("unmapped") != "unmapped" {
		t.Fatal("display name lookup changed attribution or fallback")
	}
	if !d.Hidden("old alias", "RETIRED@example.test") || d.Hidden("alex@example.test") {
		t.Fatal("visibility mismatch")
	}
	if d.Name("am", "jordan@example.test") != "Jordan Lee" {
		t.Fatal("stable identity must take precedence over a display alias")
	}
	for _, raw := range []string{`invalid`, `{"names":{"am":""}}`, `{"names":{"AM":"Alex Morgan","am":"Another Person"}}`} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("invalid directory accepted")
		}
	}
}
