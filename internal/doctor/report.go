package doctor

import "elbot/internal/config"

type FileIssue struct {
	Path   string
	Issues []config.Issue
}

type Report struct {
	ConfigPath string
	Files      []FileIssue
}

func (r *Report) add(issue config.Issue) {
	for i := range r.Files {
		if r.Files[i].Path == issue.Path {
			r.Files[i].Issues = append(r.Files[i].Issues, issue)
			return
		}
	}
	r.Files = append(r.Files, FileIssue{Path: issue.Path, Issues: []config.Issue{issue}})
}
