package confirmation

import "strings"

func riskReasonsText(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason != "" {
			if sb.Len() == 0 {
				sb.WriteByte(' ')
			} else {
				sb.WriteRune('、')
			}
			sb.WriteString(reason)
		}
	}
	return "\nrisk:" + sb.String()
}
