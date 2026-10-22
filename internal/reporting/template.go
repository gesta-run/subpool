package reporting

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"strings"
)

//go:embed template.html
var emailTemplateSource string

var emailTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"formatNumber":       number,
	"allResetsSucceeded": allResetsSucceeded,
	"compactCapacity":    compactCapacity,
}).Parse(emailTemplateSource))

func allResetsSucceeded(results []ResetResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if result.Outcome != "succeeded" {
			return false
		}
	}
	return true
}

func compactCapacity(value string) string {
	if strings.HasSuffix(value, ".0%") {
		return strings.TrimSuffix(value, ".0%") + "%"
	}
	return value
}

func Render(c Config, r Report) (Message, error) {
	var html bytes.Buffer
	if err := emailTemplate.Execute(&html, r); err != nil {
		return Message{}, err
	}
	var text strings.Builder
	if r.NoticeTitle != "" {
		fmt.Fprintf(&text, "%s\n%s\n\n", r.NoticeTitle, r.Notice)
	}
	fmt.Fprintf(&text, "Team usage overview\n%s · %s · %s\n\n", r.Date, r.Window, r.Zone)
	if r.Partial {
		text.WriteString("Partial first-day report: only the reporting window below is included. Earlier requests are not included.\n\n")
	}
	for _, a := range r.Rows {
		fmt.Fprintf(&text, "%s: %s · %s | Capacity left: %s | %s\n", a.Name, a.Requests, a.Share, a.Remaining, a.Reset)
		if a.AICredits != "" {
			fmt.Fprintf(&text, "  %s AI credits · %s reset credits\n", a.AICredits, a.ResetCredits)
			if a.CreditExpiry != "" {
				fmt.Fprintln(&text, "  "+a.CreditExpiry)
			}
		}
		for _, employee := range a.Employees {
			fmt.Fprintf(&text, "  %s: %s · %s\n", employee.Name, employee.Requests, employee.Share)
		}
	}
	fmt.Fprintf(&text, "\n%s recorded calls in this reporting window.\n", number(r.Total))
	fmt.Fprintf(&text, "\nReporting window: %s · %s\nCounts cover recorded calls through SubPool. Employee percentages are within each account; shared keys identify the key owner. Direct usage outside SubPool is excluded. Remaining capacity includes direct usage.\n", r.Window, r.Zone)
	if r.Unattributed > 0 {
		fmt.Fprintf(&text, "Calls without retained account attribution: %s (%s). These are included in the total and percentage denominator.\n", number(r.Unattributed), r.UnattributedShare)
	}
	if r.Partial {
		text.WriteString("First report: earlier requests before tracking started are not included.\n")
	}
	text.WriteString("\nOpen SubPool: " + r.URL)
	return Message{From: c.From, To: c.To, Subject: "SubPool · Team usage overview · " + r.Date, HTML: html.String(), Text: text.String()}, nil
}
