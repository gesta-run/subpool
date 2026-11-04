package modelusage

import (
	"strconv"
	"testing"
)

func TestViewRanksByTurnsAndIncludesUnknownInShares(t *testing.T) {
	// All counts are illustrative, including a repeated model row and a tie.
	s := Snapshot{Employees: []Employee{
		{ID: "alice", Name: "Alice", Models: []Model{{Model: "gpt-6.1-sol", Turns: 6}, {Model: "gpt-6.1-sol", Turns: 2}, {Model: "", Turns: 2}}},
		{ID: "bob", Name: "Bob", Models: []Model{{Model: "gpt-6.1-terra", Turns: 5}, {Model: "gpt-6.1-sol", Turns: 5}}},
		{ID: "carol", Name: "Carol"},
	}}
	v := Build(s, func(n int64) string { return strconv.FormatInt(n, 10) })
	if v.Turns != "20" || v.ModelCount != 2 || len(v.Employees) != 3 {
		t.Fatalf("view: %+v", v)
	}
	a := v.Employees[0]
	if a.ModelCount != 1 || a.Turns != "10" || a.TopModel != "GPT-6.1 Sol" || a.TopTurns != "8" || a.TopShare != "80.0%" || len(a.Models) != 2 || a.Models[1].Name != "Model not recorded" {
		t.Fatalf("employee: %+v", a)
	}
	if !v.Employees[1].Tied || len(v.Employees[2].Models) != 0 || v.Employees[2].Turns != "0" {
		t.Fatal("tie or empty employee misrepresented")
	}
}
