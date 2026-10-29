// 新增：负责执行准入、压缩、续跑预留、停止与实际退出确认。
// 按 attempt 校验归属，阻止迟到操作影响新的执行。
package lifecycle
