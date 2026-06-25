package elvena

import (
	"fmt"
	"strings"

	"elbot/internal/delivery"
)

func (t Target) ToDeliveryTarget() delivery.Target {
	out := delivery.Target{Platform: t.Platform}
	switch t.Type {
	case "private":
		out.PrivateUserID = t.ID
	case "group":
		out.GroupID = t.ID
	default:
		out.Superadmins = true
	}
	return out
}

func TargetScopeID(target Target) string {
	id := strings.TrimSpace(target.ID)
	switch strings.TrimSpace(target.Type) {
	case "private":
		if id == "" {
			return ""
		}
		if strings.TrimSpace(target.Platform) == "qqofficial" {
			return "c2c:" + id
		}
		return "private:" + id
	case "group":
		if id == "" {
			return ""
		}
		return "group:" + id
	default:
		return ""
	}
}

func SegmentsOutputs(segments []Segment, paths map[string]string) []delivery.Output {
	var out []delivery.Output
	for i, seg := range segments {
		switch seg.Kind {
		case SegmentKindText:
			out = append(out, delivery.Text(seg.Text))
		case SegmentKindImage:
			localPath := PathForSegment(i, seg, paths)
			o := delivery.ImagePath(localPath)
			o.Name = seg.Name
			out = append(out, o)
		case SegmentKindFile:
			localPath := PathForSegment(i, seg, paths)
			o := delivery.FilePath(localPath)
			o.Name = seg.Name
			out = append(out, o)
		}
	}
	return out
}

func BuildDirectOutputs(req Request, paths map[string]string) []delivery.Output {
	if len(req.Segments) == 0 {
		return []delivery.Output{delivery.Text(DirectText(req))}
	}
	return SegmentsOutputs(req.Segments, paths)
}

func DirectText(req Request) string {
	parts := []string{}
	if title := strings.TrimSpace(req.Title); title != "" {
		parts = append(parts, title)
	}
	parts = append(parts, strings.TrimSpace(req.Content))
	return strings.Join(parts, "\n")
}

func PathForSegment(i int, seg Segment, paths map[string]string) string {
	if paths != nil {
		if p, ok := paths[SegmentKey(i, seg)]; ok && p != "" {
			return p
		}
	}
	return seg.URL
}

func SegmentKey(i int, seg Segment) string {
	return fmt.Sprintf("%d:%s", i, seg.Kind)
}
