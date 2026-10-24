package share

import (
	"reflect"
	"testing"
)

func TestBuilderNormalizesMetadata(t *testing.T) {
	info := NewBuilder("example").Risk("").
		Tags(" Files ", "files", "bad/tag", "", "go.v1", "foo-bar").
		DependsOn(" first ", "", "first").DependsOn("second", " first ").BuildInfo()
	if info.Risk != RiskLow {
		t.Fatalf("risk = %q, want low", info.Risk)
	}
	if want := []string{"files", "go.v1", "foo-bar"}; !reflect.DeepEqual(info.Tags, want) {
		t.Fatalf("tags = %v, want %v", info.Tags, want)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(info.DependsOn, want) {
		t.Fatalf("dependencies = %v, want %v", info.DependsOn, want)
	}
	if got := NewBuilder("critical").Risk(RiskCritical).BuildInfo().Risk; got != RiskCritical {
		t.Fatalf("explicit risk = %q", got)
	}
}
