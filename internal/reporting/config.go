package reporting

import (
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	Enabled                 bool
	APIKey, From, PublicURL string
	To                      []string
	Location                *time.Location
	Hour, Minute            int
}

func LoadConfig(publicURL string) (Config, error) {
	c := Config{PublicURL: publicURL, Hour: 17, Minute: 50}
	var err error
	raw := strings.TrimSpace(os.Getenv("SUBPOOL_DAILY_REPORT_ENABLED"))
	if raw != "" {
		c.Enabled, err = strconv.ParseBool(raw)
		if err != nil {
			return c, fmt.Errorf("invalid daily report enabled flag")
		}
	}
	if !c.Enabled {
		return c, nil
	}
	c.APIKey = os.Getenv("SUBPOOL_RESEND_API_KEY")
	c.From = strings.TrimSpace(os.Getenv("SUBPOOL_DAILY_REPORT_FROM"))
	if c.APIKey == "" {
		return c, fmt.Errorf("SUBPOOL_RESEND_API_KEY is required for daily reports")
	}
	if _, err = mail.ParseAddress(c.From); err != nil {
		return c, fmt.Errorf("SUBPOOL_DAILY_REPORT_FROM must be a valid sender")
	}
	for _, v := range strings.Split(os.Getenv("SUBPOOL_DAILY_REPORT_TO"), ",") {
		address, parseErr := mail.ParseAddress(strings.TrimSpace(v))
		if parseErr != nil {
			return c, fmt.Errorf("SUBPOOL_DAILY_REPORT_TO must contain valid addresses")
		}
		c.To = append(c.To, address.Address)
	}
	zone := strings.TrimSpace(os.Getenv("SUBPOOL_DAILY_REPORT_TIMEZONE"))
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	c.Location, err = time.LoadLocation(zone)
	if err != nil {
		return c, fmt.Errorf("invalid daily report timezone: %w", err)
	}
	when := strings.TrimSpace(os.Getenv("SUBPOOL_DAILY_REPORT_TIME"))
	if when == "" {
		when = "17:50"
	}
	t, err := time.Parse("15:04", when)
	if err != nil {
		return c, fmt.Errorf("daily report time must be HH:MM")
	}
	c.Hour, c.Minute = t.Hour(), t.Minute()
	return c, nil
}
