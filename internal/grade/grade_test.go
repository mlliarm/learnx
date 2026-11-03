package grade

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mlliarm/learnx/internal/sandbox"
)

func result(cols int, rows ...string) *sandbox.Result {
	r := &sandbox.Result{Columns: make([]sandbox.Column, cols)}
	for _, row := range rows {
		dec := json.NewDecoder(strings.NewReader(row))
		dec.UseNumber()
		var cells []any
		if err := dec.Decode(&cells); err != nil {
			panic(err)
		}
		r.Rows = append(r.Rows, cells)
	}
	return r
}

func TestResults(t *testing.T) {
	cases := []struct {
		name      string
		want, got *sandbox.Result
		ordered   bool
		correct   bool
		reason    string
	}{
		{"identical", result(2, `[1,"a"]`, `[2,"b"]`), result(2, `[1,"a"]`, `[2,"b"]`), true, true, ""},
		{"float tolerance", result(1, `[0.3]`), result(1, `[0.30000000000000004]`), true, true, ""},
		{"float differs", result(1, `[0.3]`), result(1, `[0.31]`), true, false, "row 1 differs"},
		{"big ints exact", result(1, `[18446744073709551615]`), result(1, `[18446744073709551614]`), true, false, "row 1 differs"},
		{"unordered ignores order", result(1, `[1]`, `[2]`), result(1, `[2]`, `[1]`), false, true, ""},
		{"ordered catches order", result(1, `[1]`, `[2]`), result(1, `[2]`, `[1]`), true, false, "wrong order"},
		{"column count", result(2, `[1,2]`), result(1, `[1]`), false, false, "expected 2 columns"},
		{"row count", result(1, `[1]`, `[2]`), result(1, `[1]`), false, false, "expected 2 rows"},
		{"nested values", result(1, `[[1,2.0000000001]]`), result(1, `[[1,2]]`), true, true, ""},
		{"null vs zero", result(1, `[null]`), result(1, `[0]`), false, false, "expected a row [NULL], got [0]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Results(c.want, c.got, c.ordered)
			if v.Correct != c.correct || !strings.Contains(v.Reason, c.reason) {
				t.Fatalf("got %+v, want correct=%v reason containing %q", v, c.correct, c.reason)
			}
		})
	}
}

func TestChoice(t *testing.T) {
	choices := []string{"KQL", "ES|QL", "EQL"}
	for _, reply := range []string{"b", "B", "2", "es|ql", " ES|QL "} {
		if correct, ok := Choice(choices, "ES|QL", reply); !correct || !ok {
			t.Errorf("Choice(%q) = %v, %v; want correct", reply, correct, ok)
		}
	}
	if correct, ok := Choice(choices, "ES|QL", "a"); correct || !ok {
		t.Errorf("wrong option graded correct=%v ok=%v", correct, ok)
	}
	if _, ok := Choice(choices, "ES|QL", "d"); ok {
		t.Error("out-of-range reply accepted")
	}
}
