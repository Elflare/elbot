package share

import (
	"reflect"
	"testing"
)

func TestBuilderPreservesInputForRegistration(t *testing.T) {
	info := NewBuilder("example").Risk("").
		Tags(" Files ", "files", "bad/tag", "", "go.v1", "foo-bar").
		DependsOn(" first ", "", "first").DependsOn("second", " first ").BuildInfo()
	if info.Risk != "" {
		t.Fatalf("builder supplied implicit risk %q", info.Risk)
	}
	if want := []string{" Files ", "files", "bad/tag", "", "go.v1", "foo-bar"}; !reflect.DeepEqual(info.Tags, want) {
		t.Fatalf("tags = %v, want %v", info.Tags, want)
	}
	if want := []string{" first ", "", "first", "second", " first "}; !reflect.DeepEqual(info.DependsOn, want) {
		t.Fatalf("dependencies = %v, want %v", info.DependsOn, want)
	}
	if got := NewBuilder("critical").Risk(RiskCritical).BuildInfo().Risk; got != RiskCritical {
		t.Fatalf("explicit risk = %q", got)
	}
}
