package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/Bluestar-coder/ARTEX-RedTeam/agent"
	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/intercept"
)

// registerConversationRoutes registers the side HTTP routes. Paths, methods and
// handlers are unchanged from the pre-split table.
func (s *Server) registerConversationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chat/mentions", s.searchChatMentions)

	mux.HandleFunc("POST /api/chat", s.chat)

	mux.HandleFunc("POST /api/chat/upload", s.chatUpload) // 方式1 文件上传:落到会话/任务工作目录 uploads/

	// Agents
	// conversations (chat page)
	mux.HandleFunc("GET /api/conversations", s.pgListConversations)

	mux.HandleFunc("POST /api/conversations", s.pgCreateConversation)

	mux.HandleFunc("POST /api/conversations/delete/batch", s.pgDeleteConversationsBatch)

	mux.HandleFunc("PATCH /api/conversations/{id}", s.pgRenameConversation)

	mux.HandleFunc("PATCH /api/conversations/{id}/profile", s.pgUpdateConversation)

	mux.HandleFunc("DELETE /api/conversations/{id}", s.pgDeleteConversation)

	mux.HandleFunc("GET /api/conversations/{id}/messages", s.pgConversationMessages)

	mux.HandleFunc("POST /api/conversations/{id}/messages", s.pgSendConversationMessage)

	mux.HandleFunc("POST /api/conversations/{id}/stop", s.pgStopConversation)

	mux.HandleFunc("GET /api/conversations/{id}/messages/{seq}", s.pgConversationMsgDetail)

}

func (s *Server) registerSideRoutes(mux *http.ServeMux) {
	for _, route := range []struct{ path, kind string }{{"/api/conversations/{id}", "conversation"}, {"/api/tasks/{id}/chat", "main"}, {"/api/tasks/{id}/intents/{iid}", "worker"}} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			mux.HandleFunc(method+" "+route.path+"/side-questions", func(w http.ResponseWriter, r *http.Request) {
				p, ok := s.sideParent(w, r, route.kind)
				if ok {
					s.handleSideQuestions(w, r, p)
				}
			})
		}
	}
	mux.HandleFunc("GET /api/side-questions/{requestID}/events", s.sideEvents)
	mux.HandleFunc("POST /api/side-questions/{requestID}/cancel", s.cancelSideRequest)
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "no active task")
		return
	}
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, 409, "任务正在删除，无法发送新消息")
		return
	}
	// 注意:任务暂停(paused)不拦截主 Agent 对话。主 Agent 编排会话独立于 planner/
	// worker 的暂停,暂停中仍可继续对话(暂停只终止其正在进行的那一轮,见 control())。
	var req struct {
		Message     string           `json:"message"`
		Attachments []chatAttachment `json:"attachments,omitempty"` // 方式1 上传的文件(路径相对任务工作目录)
		Seg         *int             `json:"seg,omitempty"`         // 目标主会话分段;缺省=最新段
	}
	if !decodeBodyErr(w, r, &req, 400) {
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(w, req.Message)
	if !ok {
		return
	}
	// Admission is serialized with deletion's chat cancellation. Mark every chat
	// turn busy before its first activity/file write, including rule-mode turns.
	// If deletion wins the race, the second barrier check rejects this request.
	s.chatMu.Lock()
	if s.engine.IsDeleting(t.ID) {
		s.chatMu.Unlock()
		writeErr(w, 409, "任务正在删除，无法发送新消息")
		return
	}
	if s.chatBusy[t.ID] {
		s.chatMu.Unlock()
		writeErr(w, 409, "主 Agent 正在处理上一条消息，请稍候")
		return
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	ctx = intercept.WithReviewContext(ctx, "", intercept.ReviewBackground{Source: intercept.BackgroundUserMessage, Text: req.Message})
	s.chatBusy[t.ID] = true
	s.chatCancel[t.ID] = cancel
	s.chatMu.Unlock()

	// The turn belongs to whichever main-agent segment the user is chatting in (any
	// segment is interactive, like the top-level chat conversations). Stamp every
	// mainagent row with it so this turn's transcript + activity land in that segment.
	// Missing seg (older clients) falls back to the newest segment.
	mainSeg := 0
	if req.Seg != nil && *req.Seg >= 0 {
		mainSeg = *req.Seg
	} else {
		mainSeg, _ = t.Store.CurrentMainSeg()
	}
	segPtr := &mainSeg

	// Persist + broadcast the human turn so the 主 Agent 编排会话 survives page
	// reloads and updates live: the conversation lives in the activity stream as
	// worker="mainagent" (the per-task activity table, replayed via SSE). With
	// attachments, the activity's Detail carries {text, attachments} so the transcript
	// renders attachment cards.
	humanTurn := userActivityWithAttachments("mainagent", req.Message, req.Attachments)
	humanTurn.MainSeg = segPtr
	s.engine.emitActivity(t, humanTurn)
	var ma *agent.MainAgent
	if s.taskRuntimeAvailable(t, "mainagent") {
		ma = s.agentsForTask(t).main
	}
	if ma != nil {
		// Run the agent on a per-turn cancellable ctx (NOT r.Context()): the turn can
		// take minutes (multi-tool loop), and binding it to the request lifecycle meant
		// a page reload / proxy timeout cancelled it mid-run ("context canceled"). The
		// steps + final answer stream back live via SSE (worker="mainagent"), so the
		// handler returns immediately and the browser never needs to hold the request.
		go func() {
			defer func() {
				s.finishTaskChat(t.ID, cancel)
			}()
			// emit every step (thinking/tool_use/tool_result/text/result) so the main
			// agent session shows its work live, like worker/planner. The final answer
			// is the captured "result" step — no separate reply emit (would duplicate).
			emit := func(rec db.Activity) {
				rec.MainSeg = segPtr
				s.engine.emitActivity(t, rec)
			}
			maTaskID, _ := strconv.ParseInt(t.ID, 10, 64)
			resume := func() { s.reviveTask(t) } // set_goals 新增目标 → 把任务拉回 running
			// 把上传附件的【绝对路径】清单拼进发给 agent 的消息,它据此用 Read/Bash 打开文件。
			// taskDir = agent 的工作目录(CWD),与 chatUpload 落盘、ensureRunDir 一致。
			taskDir := filepath.Join(s.m.dir, "tasks", t.ID)
			agentMsg := composeAgentMessage(agentMessage, req.Attachments, taskDir)
			s.engine.BeginLLMCall(t.ID)
			_, err := ma.Chat(ctx, maTaskID, mainSeg, s.m.Assets(), t.Store, t.Goal, agentMsg, emit, t.Notify, resume, t.NotifyGoal, t.NotifyHint)
			s.engine.EndLLMCall(t.ID)
			if err != nil && ctx.Err() == nil {
				s.engine.emitActivity(t, db.Activity{Worker: "mainagent", Kind: "text", IsError: true, Summary: "（主 Agent 出错：" + err.Error() + "）", MainSeg: segPtr})
			}
		}()
		writeJSON(w, 202, map[string]any{"status": "accepted", "mode": "llm"})
		return
	}
	reply := s.fallbackChat(t, req.Message)
	s.engine.emitActivity(t, db.Activity{Worker: "mainagent", Kind: "text", Summary: reply, MainSeg: segPtr})
	s.finishTaskChat(t.ID, cancel)
	writeOK(w, map[string]any{"reply": reply, "mode": "rule"})
}
