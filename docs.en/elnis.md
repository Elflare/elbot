<!-- This file is auto-translated from docs/elnis.md. Do not edit manually. -->

# Elnis Listening Hub

Core Idea:

- Let ElBot control everything.
- Let Elnis manage everything.
- Let Elwisp observe everything.

Elnis is the event listening hub of ElBot. Like a star, it catches signals from the constellation of Elwisps, and ElBot then decides whether to record, notify, analyze, or execute background tasks.

## What problem does it solve

Ordinary chatbots usually only respond to user messages. Cron can respond to time, but it doesn't know what is happening in the outside world.

Elnis allows ElBot to respond to the external world.

For example:

- When a server anomaly occurs, let the LLM analyze the logs and determine whether to send a notification.
- When an RSS feed updates, automatically summarize the content and push it to the target platform.
- When a game server generates an event, let ElBot record it, send a notification, or execute background tasks.
- When a Webhook receives a business alert, pass it to ElBot first to determine the severity.
- When a local script detects a state change, send the result to ElBot instead of sending messages haphazardly.

Simply put, Elnis expands ElBot from a "bot that waits for users to speak" into an "Agent hub capable of perceiving the external world."

## Architecture Diagram

```mermaid
flowchart LR
    subgraph sources[外部世界 + Elwisp]
        direction TB
        server[服务器 / 日志<br/>Elwisp]
        rss[RSS / 网页<br/>Elwisp]
        webhook[Webhook / 业务告警<br/>Elwisp]
        game[游戏事件<br/>Elwisp]
        script[本地脚本 / 设备<br/>Elwisp]
        more[More ...<br/>Elwisp]
    end

    elvena[Elvena<br/>事件内容 / 目标期望 / 工具声明]

    subgraph elnis[Elnis 监听枢纽]
        ingress[HTTP Ingress]
        auth[Token 鉴权<br/>协议校验]
        dedupe[事件去重<br/>审计与日志]
        route[目标裁决<br/>模式分发]
    end

    record[record<br/>只记录事件]
    direct[direct<br/>直接通知]
    llm[llm<br/>后台 LLM Session]

    subgraph elbot[ElBot 控制层]
        agent[Agent]
        toolrun[ToolRun<br/>工具视图 / 风险确认 / 调用记录]
        internalTools[ElBot 内置工具]
        externalTools[Elwisp 外部工具]
        security[Security Policy]
        output[Output Layer]
    end

    platforms[目标平台<br/>CLI / QQ / 其他平台]

    server --> elvena
    rss --> elvena
    webhook --> elvena
    game --> elvena
    script --> elvena

    elvena --> ingress --> auth --> dedupe --> route
    route --> record
    route --> direct --> output
    route --> llm --> agent

    elvena -. 工具声明 / 预加载工具名 .-> toolrun
    agent -->|tool calls| toolrun
    toolrun -->|tool results| agent
    toolrun --> security
    toolrun --> internalTools
    toolrun --> externalTools
    agent --> output
    output --> platforms
```

## Three Roles

### Elnis

Elnis is the event entry runtime within ElBot, assembled at the same level as the platform runtime and Cron runtime.

It uniformly receives external events and routes them to logs, notifications, or the background LLM; like a star, all signals from Elwisp converge around it, but the final energy is controlled by ElBot.

Elnis is responsible for:

- Receiving Elvena event requests.
- Validating tokens and protocol fields.
- Deduplicate persistence based on `elwisp.name + source + id`.
- Record Elnis logs and audit information.
- Decide which platforms events can be delivered to.
- Execute `record`, `direct`, or `llm` according to the event mode.

Elnis is not an ordinary chat platform, nor does it implement PlatformAdapter. External events cannot bypass the Agent, Tool Runtime, Security Policy, and Output Layer.

### Elwisp

Elwisp is an external sub-listener. It can be a shell script, a resident process, an RSS poller, a Webhook forwarder, a game server plugin, a log listener, a hardware status collector, or any program that can convert external signals into HTTP JSON.

The "world" it observes is not limited by type, including but not limited to:

- Operating system events.
- File and log changes.
- Server status.
- Game or business service events.
- RSS, web pages, Webhooks.
- Database or queue messages.
- Devices, sensors, or local script outputs.
- Any content that a computer can receive, read, listen to, or generate.

Elwisp is like a probe scattered across the external world, and also like the eyes of ElBot. They can be numerous, small, and dispersed; they have only one responsibility: to tell Elnis what is happening in the external world.

Elwisp is only responsible for "seeing and reporting." It does not directly control ElBot, does not send messages directly to chat platforms, and does not decide whether to ultimately call an LLM or a tool.

### Elvena: event protocol

Elvena is the JSON over HTTP protocol used by Elwisp to deliver events to Elnis.

It is responsible for turning "what happened outside" into a unified event that Elnis can understand: who the source is, what the event ID is, what the content is, how it should be handled, and where it should be delivered.

Initial endpoint:

```text
POST /elvena/v1/events
GET  /healthz
```

## How events are processed

After receiving an event, Elnis determines the processing method according to `mode`.

| Mode | Function | Suitable Scenarios |
| --- | --- | --- |
| `record` | Only record events, without calling the LLM or sending notifications. | Event archiving, integration testing, low-priority signals. |
| `direct` | Directly send text notifications to the target determined by Elnis. | Simple alerts, readable messages already generated by external systems. |
| `llm` | Enters a background LLM Session, where the model analyzes it to decide whether to report. | Events that require analysis, induction, judgment, or the use of tools. |

HTTP requests in `llm` mode will return quickly, and the actual processing is executed by a background worker. The LLM eventually needs to return a structured result:

```json
{
  "completed": true,
  "need_report": true,
  "report": "处理结果"
}
```

During `need_report=true`, Elnis will send `report` according to the adjudicated target.

## Relationship with Cron, ELyph, and Skill

| Capability | Trigger Source | Function |
| --- | --- | --- |
| Cron | Time | Execute direct or LLM tasks when the time is reached. |
| Elnis | external event | Receive events delivered by Elwisp and distribute them for processing. |
| ELyph | Task text | Describe tasks, steps, and constraints in a structured manner. |
| Skill | Reusable capabilities | Condense experience or code into discoverable and callable capabilities. |

Simply put: Cron is responsible for "when to do it", Elnis is responsible for "what happened outside", ELyph is responsible for "how the task is described", and Skill is responsible for "how capabilities are reused".

## Current Limitations and Future Directions

Currently, Elnis supports record, direct, and llm modes. The following capabilities are still under development or planning:

- Elwisp declares tools along with events and is called securely by Elnis.
- Built-in Skills or tools for "creating Elwisp".
- Multiple message segments.
- Multi-turn communication between Elnis and Elwisp.
- Non-HTTP transports such as stdio, pipe, etc.
- Elnis event querying, failure retry, or disabling capabilities.

## Next Step: Configuration and Usage

After reading this, you can start learning about configuration and delivering events.

- [Elnis Configuration and Usage](elnis-usage.md): Enable Elnis, configure Elwisp policies, send Elvena requests, and understand request fields and delivery boundaries.

