package completion

import "context"

// Item is a structured completion candidate that UI layers can render without
// knowing where the candidate came from.
type Item struct {
	Text        string
	Label       string
	Description string
	Kind        string
}

type Request struct {
	Text string
}

type Source interface {
	Complete(ctx context.Context, req Request) []Item
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

func (s *Service) Complete(ctx context.Context, req Request) []Item {
	if s == nil {
		return nil
	}
	for _, source := range s.sources {
		items := source.Complete(ctx, req)
		if len(items) > 0 {
			return items
		}
	}
	return nil
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
