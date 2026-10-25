// 定义后台执行来源事实，供各业务读取。
package contextinfo

// BackgroundKind 描述已建立的后台来源；存在该事实即表示后台执行，空值表示来源未分类。
type BackgroundKind string

const (
	BackgroundKindCron  BackgroundKind = "cron"
	BackgroundKindElnis BackgroundKind = "elnis"
)
