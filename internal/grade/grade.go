// Package grade checks learner answers against reference answers.
package grade

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/mlliarm/learnx/internal/sandbox"
)

// Verdict says whether an answer is right and, when it isn't, why.
type Verdict struct {
	Correct bool
	Reason  string
}

// Results compares a learner's query result with the reference result.
// Columns are matched by position (aliases don't matter) and floats within a
// small tolerance. Row order counts only when ordered is true.
func Results(want, got *sandbox.Result, ordered bool) Verdict {
	if len(got.Columns) != len(want.Columns) {
		return Verdict{Reason: fmt.Sprintf("expected %d columns (%s), got %d (%s)",
			len(want.Columns), columnNames(want), len(got.Columns), columnNames(got))}
	}
	if len(got.Rows) != len(want.Rows) {
		return Verdict{Reason: fmt.Sprintf("expected %d rows, got %d", len(want.Rows), len(got.Rows))}
	}
	if ordered {
		if i := firstDiff(want.Rows, got.Rows); i >= 0 {
			if firstDiff(sorted(want.Rows), sorted(got.Rows)) < 0 {
				return Verdict{Reason: "the right rows, but in the wrong order"}
			}
			return Verdict{Reason: fmt.Sprintf("row %d differs: expected %s, got %s",
				i+1, FormatRow(want.Rows[i]), FormatRow(got.Rows[i]))}
		}
		return Verdict{Correct: true}
	}
	sw, sg := sorted(want.Rows), sorted(got.Rows)
	if i := firstDiff(sw, sg); i >= 0 {
		return Verdict{Reason: fmt.Sprintf("the rows differ: expected a row %s, got %s", FormatRow(sw[i]), FormatRow(sg[i]))}
	}
	return Verdict{Correct: true}
}

// Choice checks a multiple-choice reply: a letter ("b"), a number ("2") or the
// option's text. ok is false when the reply names no option.
func Choice(choices []string, answer, reply string) (correct, ok bool) {
	i := ChoiceIndex(choices, reply)
	if i < 0 {
		return false, false
	}
	return strings.EqualFold(strings.TrimSpace(choices[i]), strings.TrimSpace(answer)), true
}

// ChoiceIndex resolves a reply to an option index, or -1.
func ChoiceIndex(choices []string, reply string) int {
	r := strings.TrimSpace(reply)
	if len(r) == 1 && r[0] >= 'a' && int(r[0]-'a') < len(choices) {
		return int(r[0] - 'a')
	}
	if len(r) == 1 && r[0] >= 'A' && int(r[0]-'A') < len(choices) {
		return int(r[0] - 'A')
	}
	if n, err := strconv.Atoi(r); err == nil && n >= 1 && n <= len(choices) {
		return n - 1
	}
	for i, c := range choices {
		if strings.EqualFold(strings.TrimSpace(c), r) {
			return i
		}
	}
	return -1
}

func firstDiff(a, b [][]any) int {
	for i := range a {
		if !rowEqual(a[i], b[i]) {
			return i
		}
	}
	return -1
}

func rowEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !cellEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func cellEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		return ok && numberEqual(x, y)
	case []any:
		y, ok := b.([]any)
		return ok && rowEqual(x, y)
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if w, ok := y[k]; !ok || !cellEqual(v, w) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// numberEqual compares integers exactly (they may exceed float64 precision)
// and anything else as floats with a relative tolerance.
func numberEqual(a, b json.Number) bool {
	if a == b {
		return true
	}
	if isInteger(a) && isInteger(b) {
		return strings.TrimLeft(string(a), "+") == strings.TrimLeft(string(b), "+")
	}
	x, err1 := a.Float64()
	y, err2 := b.Float64()
	if err1 != nil || err2 != nil {
		return false
	}
	if x == y {
		return true
	}
	return math.Abs(x-y) <= 1e-9*math.Max(math.Abs(x), math.Abs(y))+1e-12
}

func isInteger(n json.Number) bool {
	return !strings.ContainsAny(string(n), ".eE")
}

// sorted orders rows by a canonical form so unordered results can be compared
// row by row. Floats are rounded in the key so tolerance survives sorting.
func sorted(rows [][]any) [][]any {
	type keyed struct {
		key string
		row []any
	}
	ks := make([]keyed, len(rows))
	for i, r := range rows {
		ks[i] = keyed{canonical(r), r}
	}
	slices.SortStableFunc(ks, func(a, b keyed) int { return strings.Compare(a.key, b.key) })
	out := make([][]any, len(ks))
	for i, k := range ks {
		out[i] = k.row
	}
	return out
}

func canonical(v any) string {
	switch x := v.(type) {
	case json.Number:
		if isInteger(x) {
			return string(x)
		}
		if f, err := x.Float64(); err == nil {
			return strconv.FormatFloat(f, 'g', 9, 64)
		}
		return string(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = canonical(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = strconv.Quote(k) + ": " + canonical(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case string:
		return strconv.Quote(x)
	case nil:
		return "NULL"
	default:
		return fmt.Sprint(x)
	}
}

// FormatRow renders a result row for feedback.
func FormatRow(r []any) string { return canonical(r) }

func columnNames(r *sandbox.Result) string {
	names := make([]string, len(r.Columns))
	for i, c := range r.Columns {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}
