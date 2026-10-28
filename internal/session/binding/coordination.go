// 职责：管理 Scope / Session 准入、租约及绑定校验。
// 维护锁顺序和可取消等待，在租约释放时交付暂存的绑定事件。
package binding
