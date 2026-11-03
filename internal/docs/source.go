// Package docs abstracts where a technology's documentation comes from.
package docs

import (
	"context"
	"errors"

	"github.com/mlliarm/learnx/internal/pack"
)

// Result is one documentation extract returned by a search.
type Result struct {
	Title string
	URL   string
	Text  string
}

// Source is a searchable documentation backend.
type Source interface {
	Search(ctx context.Context, query string) ([]Result, error)
	// Ask returns a free-form answer; sources without a QA capability
	// return ErrUnsupported.
	Ask(ctx context.Context, question string) (string, error)
	Close() error
}

var ErrUnsupported = errors.New("not supported by this docs source")

// Open picks the best available source for a pack.
func Open(ctx context.Context, p *pack.Pack) (Source, error) {
	if p.Docs.MCP != nil {
		return OpenMCP(ctx, *p.Docs.MCP)
	}
	return nil, errors.New("pack " + p.Name + " has no supported docs source")
}
