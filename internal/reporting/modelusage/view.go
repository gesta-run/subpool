package modelusage

import (
	"fmt"
	"sort"
	"strings"
)

type ModelRow struct {
	Name, Turns, Share, Color string
	Width                     float64
}

type EmployeeRow struct {
	Name, Turns, TopModel, TopTurns, TopShare string
	ModelCount                                int
	Models                                    []ModelRow
	Tied                                      bool
}

type View struct {
	Window      string
	Enabled     bool
	Unavailable bool
	Employees   []EmployeeRow
	Turns       string
	ModelCount  int
}

func Build(s Snapshot, formatNumber func(int64) string) View {
	v := View{Enabled: true, Employees: []EmployeeRow{}}
	known := map[string]bool{}
	var total int64
	for _, e := range s.Employees {
		r := employeeRow(e, formatNumber)
		v.Employees = append(v.Employees, r)
		for _, m := range e.Models {
			total += m.Turns
			if m.Turns > 0 && !unknown(m.Model) {
				known[strings.TrimSpace(m.Model)] = true
			}
		}
	}
	v.ModelCount = len(known)
	v.Turns = formatNumber(total)
	return v
}

func employeeRow(e Employee, formatNumber func(int64) string) EmployeeRow {
	r := EmployeeRow{Name: strings.TrimSpace(e.Name), Models: []ModelRow{}}
	if r.Name == "" {
		r.Name = e.Email
	}
	if r.Name == "" {
		r.Name = e.ID
	}
	counts := map[string]int64{}
	var total int64
	for _, m := range e.Models {
		name := strings.TrimSpace(m.Model)
		if unknown(name) {
			name = "unknown"
		}
		counts[name] += m.Turns
		total += m.Turns
	}
	models := make([]Model, 0, len(counts))
	for name, count := range counts {
		if count > 0 {
			models = append(models, Model{Model: name, Turns: count})
		}
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Turns != models[j].Turns {
			return models[i].Turns > models[j].Turns
		}
		return models[i].Model < models[j].Model
	})
	for _, m := range models {
		if !unknown(m.Model) {
			r.ModelCount++
		}
		share := float64(m.Turns) * 100 / float64(total)
		r.Models = append(r.Models, ModelRow{Name: label(m.Model), Turns: formatNumber(m.Turns), Share: fmt.Sprintf("%.1f%%", share), Width: share, Color: color(m.Model)})
	}
	r.Turns = formatNumber(total)
	if len(r.Models) > 0 {
		r.TopModel, r.TopTurns, r.TopShare = r.Models[0].Name, r.Models[0].Turns, r.Models[0].Share
		r.Tied = len(models) > 1 && models[0].Turns == models[1].Turns
	}
	return r
}

func unknown(name string) bool {
	return strings.TrimSpace(name) == "" || strings.EqualFold(strings.TrimSpace(name), "unknown")
}

func label(name string) string {
	switch name {
	case "gpt-6.1-sol":
		return "GPT-6.1 Sol"
	case "gpt-6.1-terra":
		return "GPT-6.1 Terra"
	case "gpt-6-astra":
		return "GPT-6 Astra"
	case "gpt-6-luna":
		return "GPT-6 Luna"
	case "unknown":
		return "Model not recorded"
	default:
		return name
	}
}

func color(name string) string {
	switch name {
	case "gpt-6.1-sol":
		return "#0768fa"
	case "gpt-6.1-terra":
		return "#168377"
	case "gpt-6-astra":
		return "#885cc4"
	case "gpt-6-luna":
		return "#b27c21"
	default:
		return "#8791a2"
	}
}
