// Package sandbox runs exercise queries in a fresh, isolated session so
// answers can be graded by executing them.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/mlliarm/learnx/internal/pack"
)

// Sandbox runs setup statements and then a query in one fresh session.
type Sandbox interface {
	// Run returns the result of query's last statement. A mistake in the SQL
	// itself is reported as a *QueryError.
	Run(ctx context.Context, setup, query string) (*Result, error)
	// Name says how the sandbox runs, e.g. "elasticsearch (docker)".
	Name() string
	// Ordered reports whether a query fixes the order of its rows, which
	// makes row order part of the answer.
	Ordered(query string) bool
}

type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Result cells are decoded JSON values: json.Number, string, bool, nil,
// []any or map[string]any.
type Result struct {
	Columns []Column
	Rows    [][]any
}

// QueryError is an error the database reported for the SQL it was given, as
// opposed to the sandbox failing to start.
type QueryError struct {
	Msg string
}

func (e *QueryError) Error() string { return e.Msg }

func IsQueryError(err error) bool {
	var qe *QueryError
	return errors.As(err, &qe)
}

// ErrNoSandbox means the pack doesn't describe a sandbox.
var ErrNoSandbox = errors.New("this pack has no sandbox, so exercises can't be run")

// Open returns the sandbox the pack describes. Slow start-up steps report
// progress to status, which may be nil.
func Open(p *pack.Pack, status io.Writer) (Sandbox, error) {
	switch p.Sandbox.Driver {
	case "":
		return nil, ErrNoSandbox
	case "elasticsearch":
		return NewElasticsearch(p.Sandbox.Image, status)
	default:
		return nil, fmt.Errorf("pack %s: unknown sandbox driver %q", p.Name, p.Sandbox.Driver)
	}
}
