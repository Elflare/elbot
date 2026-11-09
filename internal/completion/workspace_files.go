package completion

import (
	"context"
	"strings"

	"elbot/internal/workspace"
)

type WorkspaceFilesSource struct {
	Root func(context.Context) (string, error)
}

func (s WorkspaceFilesSource) Complete(ctx context.Context, req Request) ([]Item, error) {
	token, ok := workspace.ParseFileReferenceToken(req.Text, req.CursorOrEnd())
	if !ok || strings.TrimSpace(token.Query) == "" {
		return nil, nil
	}
	root, err := s.Root(ctx)
	if err != nil {
		return nil, err
	}
	files, err := workspace.SearchWorkspaceFiles(ctx, root, token.Query)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(files))
	for _, file := range files {
		items = append(items, Item{
			Text:         workspace.FormatFileReference(file.Path, token.Quoted),
			Label:        file.Path,
			Description:  "当前 Agent 工作区",
			Kind:         "workspace_file",
			ReplaceStart: token.Start,
			ReplaceEnd:   token.End,
		})
	}
	return items, nil
}
