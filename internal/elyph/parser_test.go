package elyph

import (
	"strings"
	"testing"
)

func TestParseSkillValidDocument(t *testing.T) {
	doc, err := ParseSkill(`#skill weather - 查天气
<- $city:str!
-> $remind:str
** 根据查询结果判断
~ 编造天气
$q:str = $city + 天气
$w = @tool web_search(query=$q)
=> $rain = $w 是否下雨 ? 是 : 否
`, "weather")
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
	if doc.Kind != "skill" || doc.Name != "weather" {
		t.Fatalf("doc = %#v", doc)
	}
}

func TestParseTaskValidDocument(t *testing.T) {
	doc, err := ParseTask(`#task daily_weather - 每日天气
> 查询天气
`, "daily_weather")
	if err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
	if doc.Kind != "task" || doc.Name != "daily_weather" {
		t.Fatalf("doc = %#v", doc)
	}
}

func TestParseRejectsWrongDocumentKind(t *testing.T) {
	if _, err := ParseSkill("#task daily\n", "daily"); err == nil || !strings.Contains(err.Error(), `document kind "task" does not match "skill"`) {
		t.Fatalf("ParseSkill wrong kind error = %v", err)
	}
	if _, err := ParseTask("#skill weather\n", "weather"); err == nil || !strings.Contains(err.Error(), `document kind "skill" does not match "task"`) {
		t.Fatalf("ParseTask wrong kind error = %v", err)
	}
}

func TestParseRejectsMissingOrInvalidHeader(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "\n  \n", want: "elyph is required"},
		{name: "missing header", raw: ":desc x\n> do\n", want: "first statement must be #skill <name>"},
		{name: "invalid kind", raw: "#cron daily\n", want: "first statement must be #task <name>"},
		{name: "invalid name", raw: "#skill BadName\n", want: "first statement must be #skill <name>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.name == "invalid kind" {
				_, err = ParseTask(tc.raw, "")
			} else {
				_, err = ParseSkill(tc.raw, "")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsNameMismatch(t *testing.T) {
	_, err := ParseSkill("#skill weather\n", "other")
	if err == nil || !strings.Contains(err.Error(), `#skill name "weather" does not match "other"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseRejectsReservedVariableRedefinition(t *testing.T) {
	cases := []string{"$user = 用户", "$assistant:str = elbot", "=> $user = 输入里的用户"}
	for _, line := range cases {
		raw := "#skill vars\n" + line + "\n"
		_, err := ParseSkill(raw, "vars")
		if err == nil || !strings.Contains(err.Error(), "reserved variable") || !strings.Contains(err.Error(), "line 2") {
			t.Fatalf("line %q error = %v", line, err)
		}
	}
}

func TestParseAllowsNewAssignments(t *testing.T) {
	raw := `#skill vars
$you = elbot
$me:str = 用户
$count:int = 1 + 2
$text:str = 1 + 2
$url:str = http://example.com
`
	if _, err := ParseSkill(raw, "vars"); err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
}

func TestParseValidControlBlocks(t *testing.T) {
	raw := `#task flow
?if($weather 下雨) {
  ?if($user 在外面) {
    > 提醒带伞
  }
  ?else {
    > 不提醒
  }
}
?else{
  each($day in $days, limit=3) {
    @skill weather_day(city=$city, day=$day)
  }
}
`
	if _, err := ParseTask(raw, "flow"); err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
}

func TestParseRejectsInvalidIfElseSyntax(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "if missing paren", raw: "#task bad\n?if $rain {\n}\n", want: "line 2: ?if must be ?if(condition) {"},
		{name: "if missing brace", raw: "#task bad\n?if($rain)\n", want: "line 2: ?if must be ?if(condition) {"},
		{name: "else missing question", raw: "#task bad\nelse {\n}\n", want: "line 2: else must be ?else{"},
		{name: "else spaced question", raw: "#task bad\n? else {\n}\n", want: "line 2: else must be ?else{"},
		{name: "else trailing text", raw: "#task bad\n?else now {\n}\n", want: "line 2: ?else must be ?else{"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTask(tc.raw, "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsInvalidEachSyntax(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing limit", raw: "#task bad\neach($x in $xs) {\n}\n", want: "line 2: each must be each($item in $items, limit=N) {"},
		{name: "zero limit", raw: "#task bad\neach($x in $xs, limit=0) {\n}\n", want: "line 2: each limit must be positive"},
		{name: "non variable item", raw: "#task bad\neach(x in $xs, limit=1) {\n}\n", want: "line 2: each must be each($item in $items, limit=N) {"},
		{name: "non variable list", raw: "#task bad\neach($x in xs, limit=1) {\n}\n", want: "line 2: each must be each($item in $items, limit=N) {"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTask(tc.raw, "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsBraceMismatch(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unexpected close", raw: "#task bad\n}\n", want: "line 2: unexpected }"},
		{name: "unclosed block", raw: "#task bad\n?if($rain) {\n", want: "unclosed { block"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTask(tc.raw, "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestParseOnlyTreatsLineStartCommentAsComment(t *testing.T) {
	raw := `// leading comment
#skill comments - header desc
$you = 芙莉丝 // this is literal text now
$url:str = http://example.com
?if($you 存在) {
}
`
	if _, err := ParseSkill(raw, "comments"); err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
}

func TestParseRejectsOldSyntaxAndBareNaturalLanguage(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "old desc", raw: "#skill bad\n:desc x\n", want: "line 2: line must start with a valid ELyph token"},
		{name: "old input", raw: "#skill bad\n<- city text required\n", want: "line 2: input must be <- $name:type, <- $name:type! or <- $name:type?"},
		{name: "bare language", raw: "#skill bad\n根据情况提醒\n", want: "line 2: line must start with a valid ELyph token"},
		{name: "duplicate header", raw: "#skill bad\n#task other\n", want: "line 2: header must appear only once"},
		{name: "target output unsupported", raw: "#skill bad\n>$user hi\n", want: "line 2: output must be > text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSkill(tc.raw, "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestParseDeriveTernary(t *testing.T) {
	raw := `#skill ternary
=> $rain = $w 是否下雨 ? 是 : 否
=> $city:str = $input 提到的城市
=> $title = 标题：天气提醒
=> $status = $w 下雨?是:否
`
	if _, err := ParseSkill(raw, "ternary"); err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
}

func TestParseRejectsInvalidDeriveTernary(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing false", raw: "#skill bad\n=> $x = 条件 ? 是\n", want: "line 2: ternary must be condition ? true : false"},
		{name: "empty condition", raw: "#skill bad\n=> $x = ? 是 : 否\n", want: "line 2: ternary must be condition ? true : false"},
		{name: "empty true", raw: "#skill bad\n=> $x = 条件 ? : 否\n", want: "line 2: ternary must be condition ? true : false"},
		{name: "empty false", raw: "#skill bad\n=> $x = 条件 ? 是 : \n", want: "line 2: ternary must be condition ? true : false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSkill(tc.raw, "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestParseHeaderDescriptionBoundaries(t *testing.T) {
	if _, err := ParseSkill("#skill weather  -  查天气\n", "weather"); err != nil {
		t.Fatalf("header desc should be accepted: %v", err)
	}
	cases := []struct {
		name string
		raw  string
	}{
		{name: "empty desc", raw: "#skill weather -\n"},
		{name: "old inline comment desc", raw: "#skill weather // 查天气\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseSkill(tc.raw, "weather"); err == nil || !strings.Contains(err.Error(), "first statement must be #skill <name>") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseIOBoundaries(t *testing.T) {
	valid := `#skill io
<- $required:str!
<- $optional:str?
<- $plain:str
-> $result:str
`
	if _, err := ParseSkill(valid, "io"); err != nil {
		t.Fatalf("valid IO: %v", err)
	}
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "input missing dollar", line: "<- city:str!", want: "input must be"},
		{name: "input missing type", line: "<- $city", want: "input must be"},
		{name: "input empty type", line: "<- $city:", want: "input must be"},
		{name: "input numeric type", line: "<- $city:1", want: "input must be"},
		{name: "input double marker", line: "<- $city:str!!", want: "input must be"},
		{name: "output required marker", line: "-> $result:str!", want: "output must be"},
		{name: "output optional marker", line: "-> $result:str?", want: "output must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSkill("#skill bad\n"+tc.line+"\n", "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseAssignmentBoundaries(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "empty value", line: "$x =", want: "assignment must be"},
		{name: "typed empty value", line: "$x:int =", want: "assignment must be"},
		{name: "empty type", line: "$x: = a", want: "assignment must be"},
		{name: "numeric type", line: "$x:1 = a", want: "assignment must be"},
		{name: "reserved typed", line: "$user:int = 1", want: "reserved variable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSkill("#skill bad\n"+tc.line+"\n", "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseControlBlockBoundaries(t *testing.T) {
	if _, err := ParseTask("#task ok\n?if($x) {\n}\n?else{\n}\n", "ok"); err != nil {
		t.Fatalf("?else without space should be valid: %v", err)
	}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty if", raw: "#task bad\n?if() {\n}\n", want: "line 2: ?if must be ?if(condition) {"},
		{name: "if trailing text", raw: "#task bad\n?if($x) { text\n", want: "line 2: ?if must be ?if(condition) {"},
		{name: "else spaced question", raw: "#task bad\n? else {\n}\n", want: "line 2: else must be ?else{"},
		{name: "orphan else", raw: "#task bad\n?else {\n}\n", want: "line 2: ?else must immediately follow a closed ?if block"},
		{name: "double else", raw: "#task bad\n?if($x) {\n}\n?else {\n}\n?else {\n}\n", want: "line 6: ?else must immediately follow a closed ?if block"},
		{name: "else after output", raw: "#task bad\n?if($x) {\n}\n> done\n?else {\n}\n", want: "line 5: ?else must immediately follow a closed ?if block"},
		{name: "close else same line", raw: "#task bad\n} ?else {\n", want: "line 2: line must start with a valid ELyph token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTask(tc.raw, "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseCallBoundaries(t *testing.T) {
	valid := `#skill calls
@tool web_search()
@tool web_search(query=$q)
@skill weather-day(city=$city)
`
	if _, err := ParseSkill(valid, "calls"); err != nil {
		t.Fatalf("valid calls: %v", err)
	}
	cases := []struct {
		name string
		line string
	}{
		{name: "tool missing name", line: "@tool"},
		{name: "tool uppercase", line: "@tool WebSearch(query=$q)"},
		{name: "tool missing parens", line: "@tool web_search"},
		{name: "tool missing parens around args", line: "@tool web_search query=$q"},
		{name: "tool missing value", line: "@tool web_search(query=)"},
		{name: "tool missing equals", line: "@tool web_search(query)"},
		{name: "tool duplicate comma", line: "@tool web_search(query=$q,,limit=1)"},
		{name: "tool bad key", line: "@tool web_search(1query=$q)"},
		{name: "tool nested paren", line: "@tool web_search(query=foo(bar))"},
		{name: "skill missing name", line: "@skill"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSkill("#skill bad\n"+tc.line+"\n", "bad")
			if err == nil || !strings.Contains(err.Error(), "call must be @tool/@skill name(k=v)") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseOutputBoundaries(t *testing.T) {
	if _, err := ParseSkill("#skill out\n>\n> 文本\n", "out"); err != nil {
		t.Fatalf("valid output: %v", err)
	}
	cases := []string{">text", ">$user text"}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			_, err := ParseSkill("#skill bad\n"+line+"\n", "bad")
			if err == nil || !strings.Contains(err.Error(), "output must be > text") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
func TestParseRejectsEmptyConstraintAndForbid(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "empty constraint", line: "**", want: "** must have text"},
		{name: "blank constraint", line: "**   ", want: "** must have text"},
		{name: "empty forbid", line: "~", want: "~ must have text"},
		{name: "blank forbid", line: "~   ", want: "~ must have text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSkill("#skill bad\n"+tc.line+"\n", "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseReportsMultipleDiagnostics(t *testing.T) {
	_, err := ParseTask(`#task bad
$user = x
?if $rain {
each($x in $xs) {
`, "bad")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"line 2: reserved variable", "line 3: ?if must", "line 4: each must", "unclosed { block"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestParseStepBlocks(t *testing.T) {
	raw := `#skill weather - 查天气
<- $city:str!
-> $remind:str
step fetch {
  $q:str = $city + 天气
  $w = @tool web_search(query=$q)
}
step decide {
  => $rain = $w 是否下雨 ? 是 : 否
  ?if($rain) {
    > 记得带伞
  }
  ?else {
    > 今天不用带伞
  }
}
> 完成
`
	doc, err := ParseSkill(raw, "weather")
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
	if len(doc.Steps) != 2 {
		t.Fatalf("doc.Steps = %#v, want 2", doc.Steps)
	}
	if doc.Steps[0].Name != "fetch" || doc.Steps[0].Line != 4 {
		t.Fatalf("doc.Steps[0] = %#v", doc.Steps[0])
	}
	if doc.Steps[1].Name != "decide" || doc.Steps[1].Line != 8 {
		t.Fatalf("doc.Steps[1] = %#v", doc.Steps[1])
	}
}

func TestParseStepNumericName(t *testing.T) {
	raw := `#task nums
step 1 {
  > first
}
step 2 {
  > second
}
`
	doc, err := ParseTask(raw, "nums")
	if err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
	if len(doc.Steps) != 2 || doc.Steps[0].Name != "1" || doc.Steps[1].Name != "2" {
		t.Fatalf("doc.Steps = %#v", doc.Steps)
	}
}

func TestParseStepMixesWithBareStatements(t *testing.T) {
	raw := `#task mix
step fetch {
  $w = @tool web_search(query=$q)
}
each($day in $days, limit=3) {
  @skill weather_day(city=$city, day=$day)
}
> done
`
	if _, err := ParseTask(raw, "mix"); err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
}

func TestParseRejectsInvalidStepSyntax(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing brace", raw: "#task bad\nstep fetch\n", want: "line 2: step must be step <name> {"},
		{name: "empty name", raw: "#task bad\nstep {\n}\n", want: "line 2: step must be step <name> {"},
		{name: "space in name", raw: "#task bad\nstep a b {\n}\n", want: "line 2: step must be step <name> {"},
		{name: "uppercase name", raw: "#task bad\nstep Fetch {\n}\n", want: "line 2: step name must be"},
		{name: "dot in name", raw: "#task bad\nstep 1.2 {\n}\n", want: "line 2: step name must be"},
		{name: "leading underscore", raw: "#task bad\nstep _x {\n}\n", want: "line 2: step name must be"},
		{name: "leading dash", raw: "#task bad\nstep -x {\n}\n", want: "line 2: step name must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTask(tc.raw, "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsNestedStep(t *testing.T) {
	raw := `#task bad
step outer {
  step inner {
  }
}
`
	_, err := ParseTask(raw, "bad")
	if err == nil || !strings.Contains(err.Error(), "line 3: step blocks must not be nested") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseRejectsEmptyStepBlock(t *testing.T) {
	raw := `#task bad
step empty {
}
`
	_, err := ParseTask(raw, "bad")
	if err == nil || !strings.Contains(err.Error(), "line 3: step block must not be empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseRejectsDuplicateStepName(t *testing.T) {
	raw := `#task bad
step fetch {
  > a
}
step fetch {
  > b
}
`
	_, err := ParseTask(raw, "bad")
	if err == nil || !strings.Contains(err.Error(), "line 5: step name fetch already used") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseStepWithControlFlowInside(t *testing.T) {
	raw := `#task flow
step decide {
  ?if($rain) {
    each($d in $days, limit=2) {
      > $d
    }
  }
  ?else {
    > skip
  }
}
`
	if _, err := ParseTask(raw, "flow"); err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
}
