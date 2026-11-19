package delivery

import (
	"elbot/internal/storage"
	"strings"
	"testing"
)

func TestQuestionCommandsShownOnce(t *testing.T) {
	out := QuestionsOutput([]storage.Question{{ID: 7, Question: "选哪个", Options: []string{"是"}}}, "-a", "-ac")
	for _, buttons := range []bool{false, true} {
		text := OutputBody(out, buttons)
		n := 1
		if buttons {
			n = 0
		}
		if strings.Count(text, "-a 7 1") != n || strings.Count(text, "-ac") != n || strings.Count(text, "-a 7 o") != 1 || !strings.Contains(text, "选项后可加补充") {
			t.Fatal(text)
		}
	}
}
