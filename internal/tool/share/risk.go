// 定义风险等级、评估结果及风险评估契约。
package share

import (
	"context"
	"elbot/internal/security"
)

type RiskLevel = security.RiskLevel

const (
	RiskSafe     = security.RiskSafe
	RiskLow      = security.RiskLow
	RiskMedium   = security.RiskMedium
	RiskHigh     = security.RiskHigh
	RiskCritical = security.RiskCritical
)

type RiskAssessment struct {
	Level   RiskLevel
	Reasons []string
}

type RiskAssessor interface {
	AssessRisk(ctx context.Context, req CallRequest) (RiskAssessment, error)
}
