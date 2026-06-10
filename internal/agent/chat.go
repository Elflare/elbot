package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/storage"
)

func (a *Agent) handleChat(ctx context.Context, text string) error {
	session, err := a.sessions.GetOrCreateCurrent(ctx, a.scope(ctx), text)
	if err != nil {
		return err
	}
	return a.startChat(ctx, session, text)
}

func (a *Agent) startChat(ctx context.Context, session *storage.Session, text string) error {
	if a.shouldCompact(session.ID) {
		content, err := a.compactSession(ctx, session, "auto")
		if err != nil {
			return err
		}
		a.sendChat(ctx, content)
	}
	if !a.turns.StartLLM(session.ID, text) {
		return nil
	}
	return a.runChat(ctx, session, text)
}

func (a *Agent) runChat(ctx context.Context, session *storage.Session, text string) error {
	userSegments := a.userMessageSegments(ctx, text)
	userContent := llm.SegmentsContentText(userSegments)

	userMessage := &storage.Message{
		SessionID: session.ID,
		Role:      storage.RoleUser,
		Content:   userContent,
		Metadata:  userSegmentsMetadata(userSegments),
	}

	loaded, err := a.contextLoader.Load(ctx, session.ID)
	if err != nil {
		return err
	}
	messages := append([]storage.Message{}, loaded.Messages...)
	messages = append(messages, *userMessage)

	reqCtxInfo, reqCtx, done, err := a.requests.Start(ctx, request.StartRequest{SessionID: session.ID, Kind: request.KindLLM, Label: "chat"})
	if err != nil {
		return err
	}
	defer done()
	_ = reqCtxInfo

	selection := a.modelForMode(session.Mode)
	if override, ok := ctx.Value(cronModelSelectionKey{}).(config.ModelSelection); ok {
		if override.Provider != "" {
			selection.Provider = override.Provider
		}
		if override.Model != "" {
			selection.Model = override.Model
		}
	}
	llmMessages, err := a.promptBuilder.Build(ctx, PromptBuildRequest{Session: session, Scope: a.scope(ctx), Messages: messages, Summary: loaded.Summary})
	if err != nil {
		return err
	}
	llmMessages = llm.SetLatestUserSegments(llmMessages, userSegments)
	tools, err := a.toolsForSession(ctx, session)
	if err != nil {
		return err
	}
	turnEvent, err := a.runHook(ctx, hook.Event{
		Point:   hook.PointLLMTurnPrepared,
		Session: hook.SessionContext{ID: session.ID},
		LLM: hook.LLMPayload{
			Provider: selection.Provider,
			Model:    selection.Model,
			Messages: llmMessages,
			Tools:    tools,
		},
	})
	if err != nil {
		return fmt.Errorf("llm turn hook: %w", err)
	}
	selection.Provider = turnEvent.LLM.Provider
	selection.Model = turnEvent.LLM.Model
	llmMessages = turnEvent.LLM.Messages
	tools = turnEvent.LLM.Tools
	if updatedUserContent := llm.LatestUserSegmentContentText(llmMessages); updatedUserContent != "" {
		userMessage.Content = updatedUserContent
	}

	bufferOutput := bufferAssistantOutput(ctx)
	var finalText string
	var finalRawText string
	var platformFinalText string
	var usage *llm.Usage
	turnMessages := []storage.Message{}
	toolRounds := 0
	inToolPhase := false
	latestUserContent := userMessage.Content
	for {
		if inToolPhase {
			llmMessages = a.drainPendingUserInput(session.ID, llmMessages, &turnMessages)
		}
		result, updatedUserContent, err := a.callLLM(reqCtx, session.ID, selection, llmMessages, tools, latestUserContent)
		latestUserContent = ""
		if len(result.Messages) > 0 {
			llmMessages = result.Messages
		}
		if updatedUserContent != "" {
			userMessage.Content = updatedUserContent
		}
		if err != nil {
			return err
		}
		if errors.Is(reqCtx.Err(), context.Canceled) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil
		}
		assistantText := result.Text
		assistantRawText := result.RawText
		if result.Usage != nil {
			usage = result.Usage
		}
		finalText = joinAssistantText(finalText, assistantText)
		finalRawText = joinAssistantText(finalRawText, assistantRawText)
		if err := a.sendOutputs(ctx, result.Outputs); err != nil {
			return err
		}
		if assistantText != "" && !bufferOutput {
			a.sendChat(ctx, assistantText)
		}
		if len(result.ToolCalls) == 0 {
			if inToolPhase {
				if pending := a.turns.DrainMerged(session.ID); pending != "" {
					// 用户可能在后续 LLM 响应期间补充输入；继续同一轮请求，避免 pending 被结束流程丢弃。
					llmMessages = appendAssistantTextMessage(llmMessages, assistantText, assistantRawText)
					llmMessages = appendPendingUserInput(llmMessages, &turnMessages, pending)
					continue
				}
			}
			platformFinalText = joinAssistantText(platformFinalText, assistantText)
			break
		}
		if assistantText != "" && bufferOutput {
			a.sendChat(ctx, assistantText)
		}
		if !inToolPhase {
			if !a.turns.StartToolPhase(session.ID) {
				return nil
			}
			inToolPhase = true
		}
		llmMessages = append(llmMessages, llm.LLMMessage{Role: llm.RoleAssistant, Segments: llm.TextSegments(assistantRawText), ToolCalls: result.ToolCalls})
		if toolRounds >= a.maxToolRoundsPerTurn() {
			a.sendPreview(ctx, fmt.Sprintf("已达到 max_rounds_per_turn=%d，后续工具调用未执行，正在请求模型总结当前进度。", a.maxToolRoundsPerTurn()))
			llmMessages = a.drainPendingUserInput(session.ID, llmMessages, &turnMessages)
			llmMessages = append(llmMessages, skippedToolMessages(result.ToolCalls, a.maxToolRoundsPerTurn())...)
			llmMessages = append(llmMessages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("工具调用轮次已达到上限，请基于已有工具结果和当前上下文总结当前进度，不要继续调用工具。")})
			tools = nil
			summary, _, err := a.callLLM(reqCtx, session.ID, selection, llmMessages, tools, "")
			if err != nil {
				return err
			}
			if len(summary.ToolCalls) > 0 {
				// TODO: 后续支持强制 tool_choice=none；当前总结请求已不传 tools，若仍返回工具调用则忽略。
				a.sendPreview(ctx, "总结请求仍返回了工具调用，已忽略。")
			}
			if err := a.sendOutputs(ctx, summary.Outputs); err != nil {
				return err
			}
			summaryText := summary.Text
			summaryRawText := summary.RawText
			if summaryText == "" {
				summaryText = "工具调用轮次已达到上限，当前流程已停止。"
				if summaryRawText == "" {
					summaryRawText = summaryText
				}
			}
			if summaryText != "" && !bufferOutput {
				a.sendChat(ctx, summaryText)
			}
			if summary.Usage != nil {
				usage = summary.Usage
			}
			finalText = joinAssistantText(finalText, summaryText)
			finalRawText = joinAssistantText(finalRawText, summaryRawText)
			platformFinalText = joinAssistantText(platformFinalText, summaryText)
			break
		}
		toolRounds++
		toolMessages, confirmationExtra, transcriptMessages, stopped := a.executeToolCalls(reqCtx, session, result.ToolCalls, assistantText, assistantRawText)
		if stopped {
			return nil
		}
		llmMessages = append(llmMessages, toolMessages...)
		turnMessages = append(turnMessages, transcriptMessages...)
		tools, err = a.toolsForSession(ctx, session)
		if err != nil {
			return err
		}
		if confirmationExtra != "" {
			llmMessages = append(llmMessages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("补充：" + confirmationExtra)})
		}
	}
	if !bufferOutput {
		a.sendChat(ctx, "\n")
	}

	// 工具调用消息按 OpenAI messages 形态保存；discover 结果持久化时会压缩 schema，避免历史上下文膨胀。
	assistantMessage := &storage.Message{
		SessionID: session.ID,
		Role:      storage.RoleAssistant,
		Content:   finalText,
		Metadata:  assistantRawTextMetadata(finalText, finalRawText),
	}
	if !a.turns.CompleteLLM(session.ID) {
		return nil
	}
	if err := a.store.Messages().Append(ctx, userMessage); err != nil {
		a.audit("persistence_error", "session_id", session.ID, "operation", "append_user_message", "error", err.Error())
		return err
	}
	for i := range turnMessages {
		turnMessages[i].SessionID = session.ID
		if err := a.store.Messages().Append(ctx, &turnMessages[i]); err != nil {
			a.audit("persistence_error", "session_id", session.ID, "operation", "append_tool_transcript", "error", err.Error())
			return err
		}
	}
	if err := a.store.Messages().Append(ctx, assistantMessage); err != nil {
		a.audit("persistence_error", "session_id", session.ID, "operation", "append_assistant_message", "error", err.Error())
		return err
	}
	if bufferOutput && strings.TrimSpace(platformFinalText) != "" {
		receipt, err := a.sendChatWithReceipt(ctx, platformFinalText)
		if err != nil {
			a.audit("platform_send_error", "session_id", session.ID, "operation", "send_assistant_message", "error", err.Error())
			return err
		}
		a.mapSentAssistantMessage(ctx, session.ID, assistantMessage.ID, receipt)
	}
	if err := a.sessions.Touch(ctx, session); err != nil {
		a.audit("persistence_error", "session_id", session.ID, "operation", "touch_session", "error", err.Error())
		return err
	}
	a.recordUsage(session.ID, usage)
	if a.markPendingCompact(ctx, session, usage) {
		a.sendChat(ctx, "compact status: will compact before next request\n")
	}
	a.sessions.MaybeScheduleNaming(ctx, session.ID)
	return nil
}
