// 职责：定义分支组件、所需依赖及材料准备流程。
// 通过注入的协议材料能力准备快照，并组装原子创建所需数据。
// TODO(session重构)：Bindings 由根部改接 control 提供的 share.BindingCoordinator，
// 让 Fork／压缩切换统一经过活动计时与退役流程；本包不直接依赖 control 或 binding 实现。
package branch
