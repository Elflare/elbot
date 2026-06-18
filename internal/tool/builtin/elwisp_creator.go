package builtin

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/tool"
)

const ElwispCreatorName = "elwisp_creator"

type ElwispCreatorTool struct{}

func NewElwispCreatorTool() ElwispCreatorTool {
	return ElwispCreatorTool{}
}

func (ElwispCreatorTool) Name() string {
	return ElwispCreatorName
}

func (ElwispCreatorTool) Info() tool.Info {
	return tool.NewBuilder(ElwispCreatorName).
		Description("获取创建 Elwisp 监听器所需的 Elnis/Elvena/ELyph 说明、配置片段、事件模板、代码脚手架和安全检查清单。").
		Source(tool.SourceBuiltin).
		Risk(tool.RiskLow).
		SuperadminOnly().
		DependsOn("read_file", "edit_file", "shell").
		BuildInfo()
}

func (ElwispCreatorTool) Schema() llm.ToolSchema {
	return tool.NewBuilder(ElwispCreatorName).
		Description("无参数。调用后返回创建 Elwisp 监听器的完整指南，包括 Elvena 协议、ELyph 任务写法、Elnis 配置、事件模板、代码脚手架和安全检查清单。").
		BuildSchema()
}

func (ElwispCreatorTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	_ = req
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &tool.Result{Content: elwispCreatorGuide}, nil
}

const elwispCreatorGuide = `# Elwisp Creator Guide

Use this guide to create or review an Elwisp listener for ElBot Elnis. This tool only returns instructions. To inspect existing files, use read_file. To create or modify files, use edit_file. To run checks, curl, or local scripts, use shell and follow the normal risk confirmation flow.

## Core concepts

Elnis is ElBot's internal listening hub. It receives Elvena events, authenticates tokens, validates protocol fields, deduplicates events, records logs/audit entries, resolves delivery targets, and dispatches events as record, direct, or llm.

Elwisp is an external listener. It can be a script, daemon, webhook bridge, RSS watcher, game plugin, log watcher, server monitor, or local device watcher. It observes external world events and sends Elvena JSON to Elnis.

Elvena is the JSON over HTTP event protocol used by Elwisp to report events to Elnis.

ELyph is ElBot's lightweight task notation. In Elnis llm mode, use ELyph content when the event needs structured analysis, constraints, and report decisions.

## Safety boundaries

- Elnis makes the final delivery decision. Elwisp targets are requests, not authority.
- Do not hardcode tokens in committed code. Read tokens from environment variables or local secret files.
- Do not log token values.
- Use stable event ids. Elnis deduplicates by elwisp.name + source + id.
- Prefer local binding such as 127.0.0.1 for Elnis and Elwisp tool endpoints unless the user explicitly needs remote access.
- Real file writes and shell commands must use edit_file and shell and obey ElBot risk confirmation.

## Creation workflow

1. Clarify the event source: server alert, RSS, webhook, log, script output, game event, device state, or another external system.
2. Choose mode:
   - record: only persist/log the event.
   - direct: send a simple text notification after Elnis target resolution.
   - llm: run a background LLM session for analysis, judgment, summarization, or tool use.
3. Choose identity fields:
   - elwisp.name: stable listener name, for example server-watchdog.
   - source: concrete source under that Elwisp, for example minecraft-main.
   - id: stable source-local event id. Avoid reusing ids for changed content.
4. Decide target request:
   - platforms: normally ["cli"] or ["all"].
   - superadmins: normally true.
5. Decide whether the background task needs ElBot internal tools or Skills with tool_list_names.
6. Decide whether the Elwisp needs to declare external tools in tools.
7. Inspect existing config with read_file before editing app.toml or elnis.toml.
8. Use edit_file to create listener code, config snippets, and tests.
9. Use shell for syntax checks, local test servers, or curl tests.
10. Return a concise final summary with file paths, commands, and security notes.

## Elvena v1 HTTP endpoint

Endpoint:

    POST http://<elnis-addr>/elvena/v1/events
    GET  http://<elnis-addr>/healthz

Authentication headers:

    Authorization: Bearer <token>

or:

    X-Elnis-Token: <token>

The token name is used for logs and audit only. It is not the Elwisp identity. Elwisp identity comes from elwisp.name in the JSON body.

## Elvena v1 request fields

Required fields:

- version: must be "elvena.v1".
- elwisp.name: stable Elwisp name.
- source: concrete event source.
- id: source-local event id.
- mode: "record", "direct", or "llm".
- content: event body.

Common optional fields:

- elwisp.tags: string tags for logs/statistics.
- created_at: external event time. If omitted, Elnis uses receive time.
- title: notification/session title.
- format: "text" or "elyph". Default is text.
- model_slot: llm mode slot, one of elwisp1, elwisp2, elwisp3. Falls back to work when unset or unconfigured.
- tool_list_names: ElBot internal tool names or Skill names requested for background llm mode. Tools inject schemas; Skills inject task instructions and activate their runner. Elnis allowed_tools still decides final availability. discover_tool is ignored in background.
- tools: external tool declarations supplied by Elwisp for this event.
- targets: desired delivery target. Elnis still resolves final target.
- meta: original extra metadata; useful for host, severity, URL, path, commit id, etc.

Basic direct event payload:

    {
      "version": "elvena.v1",
      "elwisp": {"name": "server-watchdog", "tags": ["server"]},
      "source": "minecraft-main",
      "id": "cpu-alert-001",
      "mode": "direct",
      "title": "服务器 CPU 异常",
      "content": "minecraft-main CPU 使用率超过阈值。",
      "targets": {"platforms": ["cli"], "superadmins": true}
    }

Basic llm event payload:

    {
      "version": "elvena.v1",
      "elwisp": {"name": "server-watchdog", "tags": ["server", "prod"]},
      "source": "minecraft-main",
      "id": "cpu-alert-001",
      "mode": "llm",
      "title": "服务器 CPU 异常",
      "format": "elyph",
      "content": "#task investigate_cpu_alert - 检查服务器 CPU 异常并判断是否需要通知\n<- $event:object!\n-> $report:str\n** 根据事件内容和可用工具判断严重程度\n~ 编造未观察到的日志或指标\n> 分析事件并在需要时生成管理员报告",
      "model_slot": "elwisp1",
      "tool_list_names": ["web_search"],
      "targets": {"platforms": ["cli"], "superadmins": true},
      "meta": {"severity": "warning", "host": "mc-main-01"}
    }

HTTP response only means Elnis accepted or rejected the event. It does not wait for LLM completion.

## Mode selection

Use record when:

- The user only wants durable event records.
- The signal is low priority or used for testing.
- No platform notification should be sent.

Use direct when:

- The external system already produced a human-readable message.
- No LLM analysis is needed.
- The event should become a simple notification after Elnis delivery policy.

Use llm when:

- The event needs judgment, summarization, filtering, or severity classification.
- The event may need tools.
- The Elwisp should not decide whether to notify.
- The user wants a structured report.

## ELyph mini guide for llm mode

Use format="elyph" and start content with #task.

Useful tokens:

- #task <name> - <description>: task header.
- <- $name:type!: required input.
- -> $name:type: output.
- ** constraint: rule to follow.
- ~ forbidden behavior: what must not happen.
- > text: output instruction.
- ?if(condition) { ... } and ?else { ... }: conditional flow.
- @tool tool_name(key=value): planned tool call in task text.

Minimal llm task:

    #task review_event - 判断事件是否需要通知
    <- $event:object!
    -> $report:str

    ** 基于事件内容、meta 和工具结果判断
    ** 报告必须简短明确
    ~ 编造不存在的日志、指标或结论
    > 分析事件。如果需要通知，给出原因和建议；否则说明无需打扰。

Do not write a huge ELyph program. Keep it short and let the LLM reason with event metadata and tools.

## Internal ElBot tools

tool_list_names requests built-in ElBot tools for llm mode. Elnis filters this list with allowed_tools from elnis.toml. Typical values:

- web_search, web_extract: search or extract web pages.
- shell: only when Elnis policy allows it and sandbox constraints are acceptable.
- read_file/edit_file: only when explicitly allowed and appropriate.

Do not include discover_tool. Background Elnis tasks do not use the default discover entry.

## Elwisp external tools

An Elwisp may declare event-local tools in the tools array. Use this when Elnis needs to ask the Elwisp side for extra data, such as service status, recent logs, or RSS item details.

Declaration example:

    {
      "name": "server_status",
      "description": "查询 minecraft-main 当前服务状态和最近错误摘要",
      "schema": {
        "type": "object",
        "properties": {
          "detail": {"type": "boolean"}
        }
      },
      "timeout_seconds": 10,
      "endpoint": "http://127.0.0.1:32171/tools/server_status"
    }

Elnis calls the endpoint with JSON:

    {
      "tool": "server_status",
      "event_key": "server-watchdog/minecraft-main/cpu-alert-001",
      "arguments": {"detail": true}
    }

Endpoint response can be plain text or JSON. JSON may use one of:

    {"content": "..."}
    {"text": "..."}
    {"result": "..."}
    {"error": "..."}

External tool risk is owned by the Elwisp environment. Elnis treats it as an external low-risk HTTP call, but Elnis policy may disable named external tools.

## elnis.toml snippet

Default disabled config:

    enabled = false
    allowed_tools = ["web_search", "web_extract"]

    [http]
    addr = "127.0.0.1:32170"
    max_body_bytes = 1048576
    queue_size = 128
    workers = 2

    [tokens.home]
    token_env = ["ELNIS_HOME_TOKEN"]

    [delivery]
    default_platforms = ["cli"]
    allow_superadmins = true

    [elwisps.server-watchdog]
    allowed_tokens = ["home"]
    allowed_tools = ["shell", "web_search"]
    disabled_external_tools = ["danger_tool"]

    [elwisps.server-watchdog.delivery]
    default_platforms = ["cli"]
    allow_superadmins = true

Enable Elnis by changing enabled to true and setting the token environment variable or .env entry.

## Python polling Elwisp template

Use this when the listener periodically checks a source and sends an event when state changes.

    import json
    import os
    import time
    import urllib.request

    ELNIS_URL = os.environ.get("ELNIS_URL", "http://127.0.0.1:32170/elvena/v1/events")
    ELNIS_TOKEN = os.environ["ELNIS_HOME_TOKEN"]

    def post_event(payload):
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        req = urllib.request.Request(
            ELNIS_URL,
            data=data,
            headers={
                "Content-Type": "application/json",
                "Authorization": "Bearer " + ELNIS_TOKEN,
            },
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=10) as resp:
            print(resp.status, resp.read().decode("utf-8"))

    def read_state():
        # TODO: replace with real observation logic.
        return {"status": "ok", "value": 1}

    def main():
        last = None
        while True:
            state = read_state()
            event_id = "state-" + str(state.get("value"))
            if state != last:
                post_event({
                    "version": "elvena.v1",
                    "elwisp": {"name": "example-poller", "tags": ["polling"]},
                    "source": "example-source",
                    "id": event_id,
                    "mode": "direct",
                    "title": "Example state changed",
                    "content": "Observed state: " + json.dumps(state, ensure_ascii=False),
                    "targets": {"platforms": ["cli"], "superadmins": True},
                    "meta": state,
                })
                last = state
            time.sleep(60)

    if __name__ == "__main__":
        main()

## Python webhook Elwisp template

Use this when another system calls the Elwisp, and the Elwisp forwards normalized Elvena events to Elnis.

    import json
    import os
    import urllib.request
    from http.server import BaseHTTPRequestHandler, HTTPServer

    ELNIS_URL = os.environ.get("ELNIS_URL", "http://127.0.0.1:32170/elvena/v1/events")
    ELNIS_TOKEN = os.environ["ELNIS_HOME_TOKEN"]

    def post_event(payload):
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        req = urllib.request.Request(
            ELNIS_URL,
            data=data,
            headers={"Content-Type": "application/json", "Authorization": "Bearer " + ELNIS_TOKEN},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, resp.read().decode("utf-8")

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length)
            incoming = json.loads(body.decode("utf-8"))
            event_id = str(incoming.get("id") or incoming.get("timestamp") or hash(body))
            payload = {
                "version": "elvena.v1",
                "elwisp": {"name": "example-webhook", "tags": ["webhook"]},
                "source": "external-webhook",
                "id": event_id,
                "mode": "llm",
                "title": incoming.get("title", "Webhook event"),
                "format": "text",
                "content": json.dumps(incoming, ensure_ascii=False),
                "targets": {"platforms": ["cli"], "superadmins": True},
                "meta": incoming,
            }
            status, text = post_event(payload)
            self.send_response(200)
            self.end_headers()
            self.wfile.write((str(status) + " " + text).encode("utf-8"))

    if __name__ == "__main__":
        HTTPServer(("127.0.0.1", 32172), Handler).serve_forever()

## External tool endpoint template

Use this only when llm mode needs to call back to Elwisp for more data.

    import json
    from http.server import BaseHTTPRequestHandler, HTTPServer

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", "0"))
            req = json.loads(self.rfile.read(length).decode("utf-8"))
            tool = req.get("tool")
            args = req.get("arguments") or {}
            if tool == "server_status":
                result = {"content": "server is running; detail=" + str(args.get("detail"))}
            else:
                result = {"error": "unknown tool: " + str(tool)}
            data = json.dumps(result, ensure_ascii=False).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(data)

    if __name__ == "__main__":
        HTTPServer(("127.0.0.1", 32171), Handler).serve_forever()

## curl tests

Health check:

    curl -sS http://127.0.0.1:32170/healthz

Direct event:

    curl -sS http://127.0.0.1:32170/elvena/v1/events -H "Content-Type: application/json" -H "Authorization: Bearer $ELNIS_HOME_TOKEN" -d "{\"version\":\"elvena.v1\",\"elwisp\":{\"name\":\"test-elwisp\"},\"source\":\"manual\",\"id\":\"test-001\",\"mode\":\"direct\",\"title\":\"Elwisp test\",\"content\":\"Hello from Elwisp\",\"targets\":{\"platforms\":[\"cli\"],\"superadmins\":true}}"

If a request returns duplicate, the same elwisp.name + source + id was already accepted. Change id for a new test event.

## Final answer format when creating an Elwisp

Return these sections:

1. Design summary: source, mode, event identity, target, tools.
2. Files created or changed.
3. Elnis config snippet and required environment variables.
4. Elwisp source code.
5. Test commands.
6. Run instructions.
7. Security checklist.

## Review checklist

- Elnis enabled only when the user wants it.
- Elnis token comes from env or .env, not committed source.
- Elwisp event id is stable and deduplicates correctly.
- Elwisp does not directly send platform messages.
- direct mode events contain human-readable content.
- llm mode content is either clear text or concise ELyph #task.
- tool_list_names only asks for tools allowed by elnis.toml.
- External tool endpoints bind locally by default.
- Generated code has timeouts for HTTP calls.
- Test curl uses the correct Elnis address and token.
`
