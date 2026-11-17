## 业务线

把以下包和5条业务线一样并列
- [ ] control
- [ ] storage
- [ ] logging
- [ ] session 

## 代码精简

目标：精简所有过度防御、无用（或者多余）变量、重复逻辑、过度抽象
构造的无意义、强行找茬测试均删
防御代码：只对竞态、io、网络等不可控地方进行防御，内部代码逻辑都假定不可能出错，真出错就交给报错方便排查
每次看一个包时，列举所有if err之类的判断
生命周期：启动失败直接报错退出；全部启动成功后才负责关闭
- [ ] dialogue包
- [ ] platform包
- [ ] tool包
- [ ] hook 包
- [ ] background包
- [ ] completion包
- [ ] fileops包
- [ ] workspace包
- [x] llm包
- [ ] media包
- [ ] storage包
- [ ] session包
- [x] command包
- [ ] control包
- [ ] delivery包

## Python 代码执行与工具编排

- [ ] 提供单一 Python 代码执行工具py_executor
  - `code`：必填，Python 源码；可在代码中直接以异步函数调用当前上下文可用的 ElBot 工具，例如 `a = await foo(i=i)`。
  - `mode`：可选，`system` 或 `uv`；省略时按 Workspace 自动选择，有 uv 项目用项目环境，否则用系统 Python。
  - `packages`：可选字符串数组，本次临时使用的第三方包；仅 uv 模式支持，不修改项目依赖文件。
  - desc的说明： 执行py代码，可直接调用当前可用的工具。
适合需要循环、批量、并行调用多个工具，或在工具之间传递、过滤、转换结果的任务。
只需要调用单个工具或执行简单 Shell 命令时，不要使用。 如： 
from pathlib import Path
import asyncio
urls = Path("urls.txt").read_text().split()
pages = await asyncio.gather(*[
    web_extract(url=url, max_chars=16000) for url in urls
])
report = []
for url, text in zip(urls, pages):
    hits = [line for line in text.splitlines() if "deprecated" in line.lower()]
    if hits:
        report.append(url + "\n" + "\n".join(hits))
Path("deprecated.txt").write_text("\n\n".join(report), encoding="utf-8")
print(f"检查 {len(urls)} 个页面，{len(report)} 个命中；已保存 deprecated.txt")
可执行工具：
自动生成，纯名字。仅已发现且声明能在执行器中用的工具会出现在这。
- 支持循环、结果处理及 `asyncio.gather` 并行调用；工具调用由 Python runner 通过 IPC 回到当前 ElBot Tool Runtime 执行，不静态解析代码猜测调用。
- 按需启动系统 Python，每次执行结束后退出，不常驻 Python worker；同一段代码中的多次工具调用共用该进程。
- 执行目录固定为当前 Workspace。uv 项目模式使用项目环境；无项目而显式指定 `uv` 时使用临时环境；`system` 模式使用系统 Python。
- 临时依赖的解析或下载需明确告知并受控。
- Python 执行风险按 Shell 最高风险处理；嵌套 ElBot 工具调用仍须经过既有 Tool Runtime 权限、确认与审计。
- 使用固定 runner：负责建立 IPC 连接、注册工具代理、执行用户代码和管理异步响应
- 把工具名注入 Python 命名空间：工具自己声明能否放入py执行器，默认不声明为否。每个允许的工具名对应一个异步代理函数。
例：
1. 多个工具联动处理

当一个任务需要先调用工具 A，再把结果交给工具 B 时，可以直接在 Python 中表达数据流，不需要模型手工拆成多轮工具调用。
issues = await search_issues(query="is:open label:bug")
for issue in issues:
    detail = await get_issue(number=issue["number"])
    print(detail)
2. 批量调用同一个工具

适用于工具只能通过 ElBot 调用、没有对应 CLI 的场景，例如先获取一批 issue，再逐个调用详情工具：

issues = await search_issues(query="is:open label=bug")
details = await asyncio.gather(*[
    get_issue_detail(issue_id=issue["id"]) for issue in issues
])
for issue in details:
    if issue["priority"] == "high" and not issue["assignee"]:
        print(issue["id"], issue["title"])

Shell 可以循环执行本地命令，但不能直接调用 ElBot 的 search_issues 和 get_issue_detail，也无法自然复用当前会话、权限和工具上下文。

3. 并行调用互不依赖的工具

当多个工具调用互相独立时，可以并发执行，减少等待时间。

results = await asyncio.gather(
    read(path="a.go", offset=1, limit=0),
    read(path="b.go", offset=1, limit=0),
    read(path="c.go", offset=1, limit=0),
)

for result in results:
    print(result)


4. 工具结果清洗

适用于工具返回的是 ElBot 内部结构化结果，不能直接通过系统命令读取；同时需要结合多个字段和嵌套数据判断：

messages = await search_messages(query="数据库连接失败")

for message in messages:
    if (
        message["author"]["type"] == "human"
        and message["channel"]["is_private"]
        and any(
            "timeout" in tag["name"].lower()
            for tag in message.get("tags", [])
        )
    ):
        print({
            "id": message["id"],
            "author": message["author"]["name"],
            "text": message["text"],
        })

这里不是简单的文本 grep，而是对工具返回的嵌套对象进行字段过滤、条件组合和结构重组。

5. 工具结果格式转换

不同工具之间的输出格式可能不同，可以在 Python 中进行转换。

raw = await web_search(query="Go JSON decoder")

for item in raw:
    print({
        "title": item["title"],
        "url": item["url"],
    })

也可以将文本解析成结构化数据：

content = await read(path="config.json", offset=1, limit=0)
config = json.loads(content)

print(config["server"]["port"])

───────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

6. 条件式工具调用

根据前一个工具的结果决定是否继续调用其他工具。

result = await search_files(pattern="*.sql")

if not result:
    print("没有找到 SQL 文件")
else:
    for path in result.splitlines():
        if path.strip():
            print(await read(path=path, offset=1, limit=0))

───────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

7. 分页或循环查询

需要多次调用同一个工具才能拿完整结果时，可以由代码维护分页状态。

page = 1

while True:
    result = await search_messages(query="部署失败", page=page)
    print(result)

    if not result["has_more"]:
        break

    page += 1

───────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

8. 使用 Shell 工具进行结果后处理

Python 负责工具编排和控制流程，Shell 负责调用 grep、jq、sort 等命令。

result = await search_logs(query="error")

filtered = await shell(
    cmd="jq -r '.[] | select(.level == \"error\") | .message'"
)
print(filtered)

不过要明确传递输入。更实际的方式是把结果写入临时文件，再交给 Shell：

result = await search_logs(query="error")

with open("logs.json", "w") as f:
    json.dump(result, f)

filtered = await shell(
    cmd="jq -r '.[] | select(.level == \"error\") | .message' logs.json"
)
print(filtered)

如果 Python 本身已经可以完成过滤，优先直接使用 Python；只有已有 Shell 命令明显更方便时，才调用 Shell。否则会把简单的数据处理绕成“Python → Shell → 子进程”，多少有点让火焰绕了一圈再烧回来。


9. 多来源结果合并

可以同时调用多个来源，再统一去重、排序或比较。

a = await web_search(query="ElBot tool runtime")
b = await web_search(query="agent tool runtime")

items = a + b
seen = set()

for item in items:
    url = item["url"]
    if url not in seen:
        seen.add(url)
        print(item["title"], url)


10. 失败重试和降级处理

Python 可以根据工具错误决定是否重试或调用备用工具。

for attempt in range(3):
    try:
        result = await fetch(url="https://example.com")
        print(result)
        break
    except RuntimeError as err:
        if attempt == 2:
            print(f"最终失败: {err}")
        else:
            await asyncio.sleep(1)


11. 生成统计摘要

适用于需要调用工具获取原始数据，再在本地计算统计结果，而不是把所有明细交给模型总结：

runs = await list_workflow_runs(status="finished")

durations = [
    run["finished_at"] - run["started_at"]
    for run in runs
    if run["started_at"] and run["finished_at"]
]

failed = sum(run["result"] == "failed" for run in runs)

print({
    "total": len(runs),
    "failed": failed,
    "success_rate": round((len(runs) - failed) / len(runs), 3),
    "average_duration": round(sum(durations) / len(durations), 2),
})

Shell 可以处理文本或 JSON 文件，但不能直接获得 ElBot 工具返回的运行记录；Python 负责调用工具和计算，最终只输出统计结果，避免把全部明细塞进上下文。

12. 使用第三方 Python 包处理工具结果

在 Workspace 有 uv 项目，或通过 packages 临时提供依赖时，可以使用第三方包进行数据处理。

import pandas as pd

result = await query_metrics(name="api_latency")
df = pd.DataFrame(result)

print(df.groupby("status")["latency"].mean())

第三方包只负责处理数据；真正的 ElBot 工具调用仍通过工具桥接回 ElBot Tool Runtime。

## 不一定实现

- [ ] CLI TUI 支持 Markdown 渲染。
- [ ] CLI 用户名和助手显示名可配置。
- [ ] CLI 支持本地图片输入。
- [ ] 周期 Cron 支持过期时间。


## 暂缓

- MCP 接入：暂不考虑，使用 Skill。
- 子 Agent：暂不考虑，信息交流会带来噪声或损失。
