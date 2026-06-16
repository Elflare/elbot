package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/config"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/output"
	"elbot/internal/platform"
	"elbot/internal/request"
	"elbot/internal/storage"
)

func (a *Agent) finishIntermediateOutput(ctx context.Context, streamCtx context.Context, stream platform.MessageStream, text string, streaming bool) error {
	if streaming {
		if strings.TrimSpace(text) != "" {
			if err := a.replaceStreamOutput(ctx, streamCtx, stream, text); err != nil {
				return err
			}
		}
		_, err := stream.Finish(streamCtx)
		return err
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if _, err := a.sendChatWithReceipt(ctx, text); err != nil {
		return err
	}
	return nil
}

func (a *Agent) replaceAndFinishStream(ctx context.Context, streamCtx context.Context, stream platform.MessageStream, text string) error {
	if err := a.replaceStreamOutput(ctx, streamCtx, stream, text); err != nil {
		return err
	}
	_, err := stream.Finish(streamCtx)
	return err
}

func splitOutputsByDeliveryTiming(outputs []output.Output) ([]output.Output, []output.Output) {
	if len(outputs) == 0 {
		return nil, nil
	}
	immediate := make([]output.Output, 0, len(outputs))
	deferred := make([]output.Output, 0)
	for _, out := range outputs {
		if output.DeliveryTiming(out) == output.DeliveryAfterAssistant {
			deferred = append(deferred, out)
			continue
		}
		immediate = append(immediate, out)
	}
	return immediate, deferred
}

func (a *Agent) replaceStreamOutput(ctx context.Context, streamCtx context.Context, stream platform.MessageStream, text string) error {
	prepared, err := a.prepareAssistantOutput(ctx, hook.PointAgentOutputPrepared, text)
	if err != nil {
		return err
	}
	if _, err := stream.Replace(streamCtx, prepared); err != nil {
		return fmt.Errorf("stream replace: %w", err)
	}
	return nil
}

func (a *Agent) startMessageStream(ctx context.Context) platform.MessageStream {
	if bufferAssistantOutput(ctx) {
		return nil
	}
	if msg, ok := platform.MessageContextFrom(ctx); ok && msg.Sender != nil {
		if sender, ok := msg.Sender.(platform.StreamingMessageSender); ok {
			stream, err := sender.StartStream(ctx)
			if err == nil {
				return stream
			}
		}
	}
	if sender, ok := a.platform.(platform.StreamingMessageSender); ok {
		stream, err := sender.StartStream(ctx)
		if err == nil {
			return stream
		}
	}
	return nil
}

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
	defer a.turns.FinishRequest(session.ID)
	if err := a.runChat(ctx, session, text); err != nil {
		a.turns.StopSession(session.ID)
		return err
	}
	return nil
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
	if a.logger != nil {
		a.logger.Info("user input", "event", "user_message", "session_id", session.ID, "text", previewLogText(userContent))
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
	if err := a.persistTurnMessage(ctx, userMessage, "append_user_message"); err != nil {
		return err
	}

	bufferOutput := bufferAssistantOutput(ctx)
	var finalText string
	var finalRawText string
	var platformFinalText string
	var finalStream platform.MessageStream
	var deferredOutputs []output.Output
	var usage *llm.Usage
	turnMessages := []storage.Message{}
	toolRounds := 0
	inToolPhase := false
	latestUserContent := userMessage.Content
	for {
		if inToolPhase {
			llmMessages = a.drainPendingUserInput(session.ID, llmMessages, &turnMessages)
			if err := a.persistTurnMessages(ctx, session.ID, "append_pending_user_message", turnMessages); err != nil {
				return err
			}
			turnMessages = turnMessages[:0]
		}
		stream := a.startMessageStream(reqCtx)
		result, updatedUserContent, err := a.callLLM(reqCtx, session.ID, selection, llmMessages, tools, latestUserContent, stream)
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
		streaming := result.Stream != nil
		if errors.Is(reqCtx.Err(), context.Canceled) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return nil
		}
		assistantText := result.Text
		assistantRawText := result.RawText
		if result.Usage != nil {
			usage = result.Usage
		}
		immediateOutputs, laterOutputs := splitOutputsByDeliveryTiming(result.Outputs)
		if len(result.ToolCalls) == 0 {
			deferredOutputs = append(deferredOutputs, laterOutputs...)
		}
		if err := a.sendOutputs(ctx, immediateOutputs); err != nil {
			return err
		}
		if len(result.ToolCalls) == 0 {
			finalText = joinAssistantText(finalText, assistantRawText)
			finalRawText = joinAssistantText(finalRawText, assistantRawText)
			if inToolPhase {
				if pending := a.turns.DrainMerged(session.ID); pending != "" {
					// 用户可能在后续 LLM 响应期间补充输入；继续同一轮请求，避免 pending 被结束流程丢弃。
					if err := a.finishIntermediateOutput(ctx, reqCtx, result.Stream, assistantText, streaming); err != nil {
						return err
					}
					llmMessages = appendAssistantTextMessage(llmMessages, assistantRawText, assistantRawText)
					llmMessages = appendPendingUserInput(llmMessages, &turnMessages, pending)
					continue
				}
			}
			platformFinalText = joinAssistantText(platformFinalText, assistantText)
			finalStream = result.Stream
			break
		}
		if err := a.finishIntermediateOutput(ctx, reqCtx, result.Stream, assistantText, streaming); err != nil {
			return err
		}
		if err := a.sendOutputs(ctx, laterOutputs); err != nil {
			return err
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
			llmMessages = append(llmMessages, skippedToolMessages(result.ToolCalls, a.maxToolRoundsPerTurn())...)
			llmMessages = a.drainPendingUserInput(session.ID, llmMessages, &turnMessages)
			if err := a.persistTurnMessages(ctx, session.ID, "append_pending_user_message", turnMessages); err != nil {
				return err
			}
			turnMessages = turnMessages[:0]
			llmMessages = append(llmMessages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("工具调用轮次已达到上限，可以询问用户是否继续或者基于已有工具结果和当前上下文总结当前进度。")})
			tools = nil
			stream := a.startMessageStream(reqCtx)
			summary, _, err := a.callLLM(reqCtx, session.ID, selection, llmMessages, tools, "", stream)
			if err != nil {
				return err
			}
			if len(summary.ToolCalls) > 0 {
				// TODO: 后续支持强制 tool_choice=none；当前总结请求已不传 tools，若仍返回工具调用则忽略。
				a.sendPreview(ctx, "总结请求仍返回了工具调用，已忽略。")
			}
			immediateOutputs, laterOutputs := splitOutputsByDeliveryTiming(summary.Outputs)
			deferredOutputs = append(deferredOutputs, laterOutputs...)
			if err := a.sendOutputs(ctx, immediateOutputs); err != nil {
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
			if summary.Usage != nil {
				usage = summary.Usage
			}
			finalText = joinAssistantText(finalText, summaryRawText)
			finalRawText = joinAssistantText(finalRawText, summaryRawText)
			platformFinalText = joinAssistantText(platformFinalText, summaryText)
			finalStream = summary.Stream
			break
		}
		toolRounds++
		toolMessages, confirmationExtra, transcriptMessages, stopped := a.executeToolCalls(reqCtx, session, result.ToolCalls, assistantRawText, assistantRawText)
		llmMessages = append(llmMessages, toolMessages...)
		if err := a.persistTurnMessages(ctx, session.ID, "append_tool_transcript", transcriptMessages); err != nil {
			return err
		}
		if stopped {
			return nil
		}
		tools, err = a.toolsForSession(ctx, session)
		if err != nil {
			return err
		}
		if confirmationExtra != "" {
			llmMessages = append(llmMessages, llm.LLMMessage{Role: llm.RoleUser, Segments: llm.TextSegments("补充：" + confirmationExtra)})
		}
	}
	platformOutputText := platformFinalText
	if strings.TrimSpace(platformOutputText) != "" {
		var err error
		platformOutputText, err = a.prepareAssistantOutput(ctx, hook.PointAgentTurnOutputPrepared, platformOutputText)
		if err != nil {
			return fmt.Errorf("turn output hook: %w", err)
		}
		if !bufferOutput {
			if finalStream != nil {
				if err := a.replaceAndFinishStream(ctx, reqCtx, finalStream, platformOutputText); err != nil {
					return err
				}
			} else if _, err := a.sendChatWithReceipt(ctx, platformOutputText); err != nil {
				return err
			}
		}
	}

	if !bufferOutput {
		if err := a.sendOutputs(ctx, deferredOutputs); err != nil {
			return err
		}
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
	if err := a.persistTurnMessage(ctx, assistantMessage, "append_assistant_message"); err != nil {
		return err
	}
	if bufferOutput {
		if strings.TrimSpace(platformOutputText) != "" {
			receipt, err := a.sendChatWithReceipt(ctx, platformOutputText)
			if err != nil {
				a.audit("platform_send_error", "session_id", session.ID, "operation", "send_assistant_message", "error", err.Error())
				return err
			}
			a.mapSentAssistantMessage(ctx, session.ID, assistantMessage.ID, receipt)
		}
		if err := a.sendOutputs(ctx, deferredOutputs); err != nil {
			return err
		}
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
