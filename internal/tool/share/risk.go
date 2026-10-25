// 定义工具准备阶段使用的风险等级与评估结果。
package share

import "elbot/internal/security"

type RiskLevel = security.RiskLevel

const (
	RiskSafe     = security.RiskSafe
	RiskLow      = security.RiskLow
	RiskMedium   = security.RiskMedium
	RiskHigh     = security.RiskHigh
	RiskCritical = security.RiskCritical
)

type RiskAssessment struct {
	Level   RiskLevel // 本次准备时确定的风险等级，供权限判断和确认展示使用。
	Reasons []string  // 风险判断的说明，与本次调用的准备结果一同固定。
}
