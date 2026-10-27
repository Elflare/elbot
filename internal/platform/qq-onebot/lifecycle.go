// lifecycle.go 负责适配器启停、重连、读循环及在途入站工作的登记与等待。
// TODO：待实现分阶段生命周期，停止业务接入后保留连接读取与发送，最终 Close 再释放连接。

package qqonebot
