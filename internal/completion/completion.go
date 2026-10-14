package completion

import "context"

// Item is a structured completion candidate that UI layers can render without
// knowing where the candidate came from.
type Item struct {
	Text         string
	Label        string
	Description  string
	Kind         string
	ReplaceStart int
	ReplaceEnd   int
}

type Request struct {
	Text   string
	Cursor int
}

func (r Request) CursorOrEnd() int {
	if r.Cursor <= 0 || r.Cursor > len(r.Text) {
		return len(r.Text)
	}
	return r.Cursor
}

type Source interface {
	Complete(ctx context.Context, req Request) ([]Item, error)
}

type Service struct {
	sources []Source
}

func NewService(sources ...Source) *Service {
	out := make([]Source, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			out = append(out, source)
		}
	}
	return &Service{sources: out}
}

func (s *Service) Complete(ctx context.Context, req Request) ([]Item, error) {
	for _, source := range s.sources {
		items, err := source.Complete(ctx, req)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			return items, nil
		}
	}
	return nil, nil
}

func Texts(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item.Text != "" {
			out = append(out, item.Text)
		}
	}
	return out
}
