// lifecycle.go 将负责分阶段启停、重连调度、业务退出等待及最终资源清理。
// TODO：从旧 Run 迁入运行协调，实现 Start、BeginClose、Wait、Close；当前仅为职责骨架。

package qqofficial
