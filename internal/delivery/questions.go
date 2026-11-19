package delivery

import (
	"elbot/internal/storage"
	"fmt"
	"strings"
)

type QuestionContent struct {
	storage.Question
	AnswerCommand string `json:"answer_command"` // Configured command including its prefix.
}

func (q QuestionContent) text() string {
	var text strings.Builder
	fmt.Fprintf(&text, "#%d: %s", q.ID, q.Question.Question)
	for i, option := range q.Options {
		fmt.Fprintf(&text, "\n%s %d %d  %s", q.AnswerCommand, q.ID, i+1, option)
	}
	fmt.Fprintf(&text, "\n%s %d o <回答>", q.AnswerCommand, q.ID)
	if d := q.Draft; d != nil {
		answer := d.Text
		if d.Choice != nil {
			answer = q.Options[*d.Choice-1]
			if d.Text != "" {
				answer += " · " + d.Text
			}
		}
		fmt.Fprintf(&text, "\n草稿：%s", answer)
	}
	return text.String()
}

func QuestionsOutput(questions []storage.Question, answerCommand, confirmCommand string) Output {
	out := Output{Kind: KindText}
	for _, q := range questions {
		out.Blocks = append(out.Blocks, ContentBlock{Kind: ContentQuestion, Content: QuestionContent{Question: q, AnswerCommand: answerCommand}})
		for i, option := range q.Options {
			out.Actions = append(out.Actions, Action{ID: fmt.Sprintf("a-%d-%d", q.ID, i+1), Label: fmt.Sprintf("#%d · %s", q.ID, option), Command: fmt.Sprintf("%s %d %d", answerCommand, q.ID, i+1)})
		}
	}
	out.Blocks = append(out.Blocks, ContentBlock{Kind: ContentText, Content: "选项后可加补充 · " + confirmCommand + " 提交草稿"})
	out.Actions = append(out.Actions, Action{ID: "ac", Label: "提交草稿", Command: confirmCommand})
	out.Text = ContentBlocksText(out.Blocks)
	return out
}
