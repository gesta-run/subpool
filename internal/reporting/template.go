package reporting

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

var emailTemplate = template.Must(template.New("report").Parse(`<!doctype html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"></head><body style="margin:0;background:#f5f6f8;color:#151922;font-family:Arial,Helvetica,sans-serif"><table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr><td align="center" style="padding:32px 12px"><table role="presentation" width="680" cellspacing="0" cellpadding="0" style="width:100%;max-width:680px;background:white;border:1px solid #dce0e6;border-radius:12px"><tr><td style="padding:32px 28px">
<div style="font-size:30px;font-weight:800;letter-spacing:-1px">sub<span style="color:#0768fa">pool</span></div>
<h1 style="font-size:30px;line-height:1.2;margin:32px 0 12px">Team usage overview</h1>
<p style="font-size:17px;color:#596273;margin:0 0 24px">{{.Date}} · {{.Time}} · {{.Zone}}</p>
<p style="font-size:18px;line-height:1.5">Today’s SubPool traffic and the capacity available to the team.</p>
<table width="100%" cellspacing="0" cellpadding="0" style="border-collapse:collapse;margin:24px 0;font-size:18px"><thead><tr style="background:#f3f5f8;color:#596273;text-align:left;font-size:13px"><th style="padding:14px 10px;border:1px solid #dce0e6">ACCOUNT</th><th style="padding:14px 10px;border:1px solid #dce0e6">TODAY’S REQUESTS</th><th style="padding:14px 10px;border:1px solid #dce0e6">WEEKLY CAPACITY LEFT</th></tr></thead><tbody>
{{range .Rows}}<tr><td style="padding:22px 10px;border:1px solid #dce0e6;font-weight:700">{{.Name}}</td><td style="padding:22px 10px;border:1px solid #dce0e6"><span style="font-size:20px;font-weight:700">{{.Requests}}</span><span style="font-size:20px;font-weight:700"> · {{.Share}}</span></td><td style="padding:22px 10px;border:1px solid #dce0e6"><strong style="font-size:22px">{{.Remaining}}</strong><table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="margin:8px 0;background:#e8edf3;border-radius:6px"><tr><td style="height:8px;width:{{.Bar}}%;background:#0768fa;border-radius:6px"></td><td style="height:8px"></td></tr></table><div style="font-size:14px;color:#596273;line-height:1.4">{{.Reset}}</div></td></tr>{{end}}
</tbody></table><p style="font-size:19px">{{.Total}} team requests handled in this reporting window.</p>
<div style="padding:16px;background:#edf4ff;border-radius:8px;font-size:16px;line-height:1.5">Requests cover SubPool traffic only. Remaining capacity also reflects direct account usage.</div>
{{if .Partial}}<p style="font-size:16px;line-height:1.5;color:#596273">First report: request tracking started today at the beginning of the reporting window. Earlier requests are not included.</p>{{end}}
<p style="margin:28px 0"><a href="{{.URL}}" style="display:inline-block;padding:14px 22px;background:#0768fa;color:white;text-decoration:none;border-radius:7px;font-size:18px;font-weight:600">Open SubPool</a></p>
<div style="border-top:1px solid #dce0e6;padding-top:18px;color:#596273;font-size:14px;line-height:1.6">Reporting window: {{.Window}} · {{.Zone}}<br>Sent daily by SubPool</div>
</td></tr></table></td></tr></table></body></html>`))

func Render(c Config, r Report) (Message, error) {
	var html bytes.Buffer
	if err := emailTemplate.Execute(&html, r); err != nil {
		return Message{}, err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Team usage overview\n%s · %s · %s\n\n", r.Date, r.Time, r.Zone)
	for _, a := range r.Rows {
		fmt.Fprintf(&text, "%s: %s · %s | Weekly capacity left: %s | %s\n", a.Name, a.Requests, a.Share, a.Remaining, a.Reset)
	}
	fmt.Fprintf(&text, "\nReporting window: %s · %s\nRequests cover completed SubPool responses only. Remaining capacity also reflects direct account usage.\n", r.Window, r.Zone)
	if r.Partial {
		text.WriteString("First report: earlier requests before tracking started are not included.\n")
	}
	text.WriteString("\nOpen SubPool: " + r.URL)
	return Message{From: c.From, To: c.To, Subject: "SubPool · Team usage overview · " + r.Date, HTML: html.String(), Text: text.String()}, nil
}
