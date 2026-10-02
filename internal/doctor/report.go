package doctor

import (
	"fmt"
	"strings"

	"elbot/internal/config"
)

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

// Text renders the diagnosis as a request the user can copy to Elbot.
func (r Report) Text() string {
	if len(r.Files) == 0 {
		return "Everything is OK"
	}
	var errors, hints int
	var elnis bool
	for _, file := range r.Files {
		for _, issue := range file.Issues {
			if issue.Level == config.LevelError {
				errors++
			} else {
				hints++
			}
			elnis = elnis || issue.Topic == config.TopicElnis
		}
	}
	var out strings.Builder
	if errors == 0 {
		fmt.Fprintf(&out, "未发现必要问题，有 %d 项提示，涉及 %d 个文件。", hints, len(r.Files))
	} else {
		fmt.Fprintf(&out, "发现 %d 项错误、%d 项提示，涉及 %d 个文件。", errors, hints, len(r.Files))
	}
	out.WriteString("请复制以下内容给 Elbot：\n\n")
	out.WriteString("请参考以下资料，处理列出的 ElBot 配置问题：\n")
	out.WriteString("配置说明：https://raw.githubusercontent.com/Elflare/elbot/main/docs/configuration.md\n")
	out.WriteString("默认模板：https://raw.githubusercontent.com/Elflare/elbot/main/internal/config/assets.go\n")
	if elnis {
		out.WriteString("Elnis 说明：https://raw.githubusercontent.com/Elflare/elbot/main/docs/elnis.md\n")
	}
	fmt.Fprintf(&out, "\n主配置：%s\n\n检查结果：\n", r.ConfigPath)
	for i, file := range r.Files {
		if i > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "%d. %s\n", i+1, file.Path)
		for _, issue := range file.Issues {
			label := "提示"
			if issue.Level == config.LevelError {
				label = "错误"
			}
			fmt.Fprintf(&out, "   - [%s] %s\n", label, issue.Message)
		}
	}
	out.WriteString("\n修改前请备份；补齐必要配置及对应注释，保留已有配置值和注释；可选项先说明作用，由用户决定是否补充；格式错误、重复定义和未知字段先说明处理建议，Skill 差异只说明、不覆盖。")
	return out.String()
}
