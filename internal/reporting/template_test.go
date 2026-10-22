package reporting

import (
	"strings"
	"testing"
)

func TestResetNoticeKeepsMixedOutcomesAndEscapesDetails(t *testing.T) {
	// All identities, capacities and details are illustrative.
	r := Report{NoticeTitle: "Reset results", ResetResults: []ResetResult{
		{Account: "first@example.test", Outcome: "succeeded", Before: "25.0%", After: "100.0%"},
		{Account: "second@example.test", Outcome: "unconfirmed", Before: "40.0%", After: "Unavailable", Detail: "Check <provider> before retrying"},
	}}
	m, err := Render(fixtureConfig(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Review needed", "succeeded", "unconfirmed", "Check &lt;provider&gt; before retrying", "25% → 100%", "Unavailable"} {
		if !strings.Contains(m.HTML, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(m.HTML, "✓ Resets completed") {
		t.Fatal("mixed outcomes presented as success")
	}
	r.ResetResults = r.ResetResults[:1]
	m, err = Render(fixtureConfig(), r)
	if err != nil || !strings.Contains(m.HTML, "✓ Resets completed") {
		t.Fatal("confirmed success banner missing", err)
	}
}
