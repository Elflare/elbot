// 运行 Skill 子进程，统一收集有界 stdout、stderr 并处理退出结果。
// 进程环境与取消机制复用公共 processenv，媒体结果转换交给 media.go。
package skill
