package server

// Synthetic conversation generator for performance testing. It builds a
// conversation containing every message and tool-call shape the UI can
// render (thinking, text, bash, patch/diff, keyword_search, change_dir,
// subagent, web_search, browser, llm_one_shot, output_iframe, plus
// gitinfo/warning/error/modelchange markers) so we can load large
// conversations and measure client + server performance without needing a
// live model. Reachable at /debug/loremipsum?size=... (see sizePresets).

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// sizePresets maps friendly size names to a turn count. A "turn" is one
// user prompt followed by the agent's thinking/text and a batch of tool
// calls with results, so each turn is ~4-5 messages.
//
// Sizes are calibrated against the real Shelley database (as of this
// writing): the largest real conversation is ~2,700 messages / ~14 MB of
// message JSON, and p99 is ~900 messages / ~6.6 MB. The lower presets track
// those percentiles; the upper presets (huge/giant) deliberately overshoot
// the real max several-fold so we have headroom to find where load/render
// perf breaks. Turn counts translate to roughly 4.5x as many messages.
var sizePresets = map[string]int{
	"tiny":   2,     // ~10 messages
	"small":  10,    // ~45 messages (~real p50)
	"medium": 50,    // ~230 messages (~real p85)
	"large":  250,   // ~1,100 messages (~real p99)
	"xlarge": 1000,  // ~4,500 messages (~1.7x real max)
	"huge":   5000,  // ~22,000 messages (~8x real max)
	"giant":  15000, // ~67,000 messages (~25x real max)
}

// maxTurns caps generation so a single request can't exhaust memory/disk.
// 100k turns is ~450k messages; well past any realistic perf target.
const maxTurns = 100000

// presetNames is the human-facing list of valid preset names, kept in sync
// with sizePresets for error messages.
const presetNames = "tiny/small/medium/large/xlarge/huge/giant"

// handleDebugLoremIpsum renders a landing page on GET and generates a
// synthetic conversation on POST. Generation has side effects (it writes a
// conversation), so it must not run on a bare GET. size may be a preset name
// (see sizePresets) or a raw turn count; optional model overrides the stored
// model name. A POST with ?json=1 returns the conversation id as JSON instead
// of redirecting.
func (s *Server) handleDebugLoremIpsum(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.renderLoremIpsumLanding(w, "")
		return
	}

	size := strings.TrimSpace(r.FormValue("size"))
	if size == "" {
		size = "medium"
	}
	turns, ok := sizePresets[strings.ToLower(size)]
	if !ok {
		n, err := strconv.Atoi(size)
		if err != nil || n < 1 {
			s.renderLoremIpsumLanding(w, fmt.Sprintf("Invalid size %q: use a turn count or one of "+presetNames+".", size))
			return
		}
		turns = n
	}
	// Guard against absurd sizes that could exhaust memory/disk.
	if turns > maxTurns {
		s.renderLoremIpsumLanding(w, fmt.Sprintf("Size too large: max %d turns.", maxTurns))
		return
	}

	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		model = s.effectiveDefaultModel(s.getModelList())
	}

	// Detach from the request context so a client disconnect mid-generation
	// (large sizes can take many seconds) doesn't cancel the writes and leave
	// a partially-populated conversation behind.
	ctx := context.WithoutCancel(r.Context())
	convID, err := s.generateLoremConversation(ctx, turns, model)
	if err != nil {
		s.logger.Error("loremipsum generation failed", "error", err)
		http.Error(w, fmt.Sprintf("failed to generate conversation: %v", err), http.StatusInternalServerError)
		return
	}

	// A programmatic client can pass ?json=1 to get the id back; a browser
	// form submission navigates to the new conversation.
	if r.URL.Query().Get("json") != "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"conversation_id": convID,
			"turns":           turns,
			"url":             "/c/" + convID,
		})
		return
	}
	http.Redirect(w, r, "/c/"+convID, http.StatusSeeOther)
}

// renderLoremIpsumLanding writes the landing page, optionally with an error
// banner. It lists the presets (name, turn count, and approximate message
// count) as POST buttons plus a custom-count form.
func (s *Server) renderLoremIpsumLanding(w http.ResponseWriter, errMsg string) {
	var rows strings.Builder
	for _, name := range presetOrder {
		turns := sizePresets[name]
		rows.WriteString(fmt.Sprintf(
			`<tr><td class="name">%s</td><td class="turns">%s turns</td><td class="msgs">~%s messages</td>`+
				`<td><form method="post"><input type="hidden" name="size" value="%s">`+
				`<button type="submit">Generate</button></form></td></tr>`,
			name, commaInt(turns), commaInt(approxMessages(turns)), name,
		))
	}

	banner := ""
	if errMsg != "" {
		banner = `<div class="banner">` + template.HTMLEscapeString(errMsg) + `</div>`
	}

	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	// Single-pass replace so neither substitution can match a placeholder
	// introduced by the other (e.g. a crafted error message).
	html := strings.NewReplacer("__BANNER__", banner, "__ROWS__", rows.String()).Replace(debugLoremIpsumHTML)
	w.Write([]byte(html))
}

// presetOrder lists preset names smallest-to-largest for stable display.
var presetOrder = []string{"tiny", "small", "medium", "large", "xlarge", "huge", "giant"}

// approxMessages estimates the message count a turn count produces, for the
// landing-page "Approx." column. Each turn is ~4-5 messages (see the
// sizePresets comment); round to 4.5x, floored to a round number for display.
func approxMessages(turns int) int {
	n := turns * 9 / 2
	if n >= 1000 {
		n = n / 1000 * 1000 // round down to the nearest thousand
	}
	return n
}

// commaInt formats an int with thousands separators.
func commaInt(n int) string {
	s := strconv.Itoa(n)
	if n < 1000 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// generateLoremConversation creates a conversation and fills it with turns
// batches of synthetic messages. Returns the conversation id.
func (s *Server) generateLoremConversation(ctx context.Context, turns int, model string) (string, error) {
	cwd := "/home/exedev/exe-loremipsum"
	conv, err := s.db.CreateConversation(ctx, nil, true, &cwd, &model, db.ConversationOptions{
		ThinkingLevel: "medium",
		// Synthetic data should never ping the user.
		DisableNotifications: true,
	})
	if err != nil {
		return "", fmt.Errorf("create conversation: %w", err)
	}
	convID := conv.ConversationID

	// A stable, human-scannable slug. conversation_id keeps it unique.
	slug := fmt.Sprintf("loremipsum-%dturns-%s", turns, convID)
	if _, err := s.db.UpdateConversationSlug(ctx, convID, slug); err != nil {
		return "", fmt.Errorf("set slug: %w", err)
	}

	g := &loremGen{s: s, convID: convID, model: model, cwd: cwd, generation: 1, clock: time.Now().Add(-time.Duration(turns) * time.Minute)}

	// One-time preamble: system prompt + a gitinfo marker, flushed as its
	// own batch before the turns begin.
	g.recordTyped(db.MessageTypeSystem, g.systemMessage(), llm.Usage{}, nil)
	g.recordTyped(db.MessageTypeGitInfo, g.gitInfoMessage(), llm.Usage{}, g.gitInfoUserData())
	if err := g.flush(ctx); err != nil {
		return "", err
	}

	// Compaction cadence. Real conversations compact rarely (the busiest in
	// the DB reach generation ~6). We want generation dividers and compaction
	// summaries to actually appear, but not hundreds of them, so we target a
	// bounded number of compactions (~maxCompactions) spread across the
	// conversation. For a small conversation that would never otherwise
	// compact, compact once in the middle so the feature is still represented.
	const minCompactInterval = 40 // never compact more often than this
	const maxCompactions = 8
	compactEvery := minCompactInterval
	if turns/maxCompactions > compactEvery {
		compactEvery = turns / maxCompactions
	}
	if turns >= 6 && turns <= minCompactInterval {
		compactEvery = turns / 2
	}

	for i := 0; i < turns; i++ {
		if err := g.turn(ctx, i); err != nil {
			return "", fmt.Errorf("turn %d: %w", i, err)
		}
		// Compact after this turn (but never on the very last turn, so the
		// final generation always has real turns of its own).
		if compactEvery > 0 && (i+1)%compactEvery == 0 && i+1 < turns {
			if err := g.compact(ctx, i); err != nil {
				return "", fmt.Errorf("compact after turn %d: %w", i, err)
			}
		}
	}
	return convID, nil
}

// loremGen holds per-conversation generation state.
type loremGen struct {
	s      *Server
	convID string
	model  string
	cwd    string
	// generation is the conversation's current generation. It starts at 1
	// and is bumped by compact(), mirroring the real distillation flow so
	// generated rows carry the right generation stamp and the UI draws a
	// "new generation" divider.
	generation int
	// clock advances a few seconds per message so timestamps and tool
	// durations look realistic and monotonic.
	clock time.Time
	// pending buffers a turn's worth of message params so they can be
	// written in a single transaction via CreateMessages. Writing each
	// message in its own Tx would fire a conversation-list recompute + SSE
	// publish per row, which is exactly the slowness CreateMessages exists
	// to avoid — and defeats the purpose of a perf-test generator.
	pending []db.CreateMessageParams
}

func (g *loremGen) tick(d time.Duration) time.Time {
	g.clock = g.clock.Add(d)
	return g.clock
}

// record queues one synthetic message, reusing the server's real
// message-type detection so the stored rows are the same shape the loop
// would have written.
func (g *loremGen) record(msg llm.Message, usage llm.Usage, userData any) error {
	messageType, err := g.s.getMessageType(msg)
	if err != nil {
		return err
	}
	g.recordTyped(messageType, msg, usage, userData)
	return nil
}

// recordTyped is record with an explicit message type, used for the
// user-visible-only marker types (gitinfo, warning, modelchange) that are
// not inferable from the llm.Message alone.
func (g *loremGen) recordTyped(messageType db.MessageType, msg llm.Message, usage llm.Usage, userData any) {
	// Mirror buildCreateMessageParams' error user_data stamping so error
	// messages render their Retry affordance.
	if msg.ErrorType != llm.ErrorTypeNone && userData == nil {
		userData = map[string]any{
			"error_type": string(msg.ErrorType),
			"retryable":  msg.ErrorRetryable,
		}
	}
	markAgentDone := (messageType == db.MessageTypeAgent || messageType == db.MessageTypeError) && msg.EndOfTurn
	// Stamp created_at from the same synthetic clock as this message's own
	// tool/usage timestamps (see usage() and turn()'s per-tool ticks), not
	// the DB default (real insertion time) — see db.CreateMessageParams.CreatedAt.
	createdAt := g.tick(200 * time.Millisecond)
	g.pending = append(g.pending, db.CreateMessageParams{
		ConversationID:      g.convID,
		Type:                messageType,
		LLMData:             msg,
		UserData:            userData,
		UsageData:           usage,
		LLMAPIURL:           usage.URL,
		ModelName:           usage.Model,
		DisplayData:         ExtractDisplayData(msg),
		ExcludedFromContext: msg.ExcludedFromContext,
		MarkAgentDone:       markAgentDone,
		CreatedAt:           &createdAt,
		// No BumpTimestamp: flush's CreateMessages bumps the conversation
		// timestamp once at the end of the batch Tx, so a per-message bump
		// would be a redundant write.
	})
}

// flush writes all queued messages for the current turn in a single
// transaction (one commit hook, one list recompute) and clears the buffer.
func (g *loremGen) flush(ctx context.Context) error {
	if len(g.pending) == 0 {
		return nil
	}
	if _, err := g.s.db.CreateMessages(ctx, g.pending); err != nil {
		return err
	}
	g.pending = g.pending[:0]
	return nil
}

// compact simulates a conversation compaction (pi-style distillation) after
// turn i. It mirrors the real flow in distill.go/distill_pi.go: bump the
// conversation's generation, then in the NEW generation write an in_progress
// distill status, a fresh system prompt, the compaction summary, a handful of
// verbatim carried-forward messages, and a terminal "complete" status. Because
// insertMessageTx stamps each row with the conversation's current generation,
// bumping first lands all of these in the new generation, so the UI draws a
// "new generation" divider, shows the compaction summary, and collapses the
// carried tail behind a "messages carried forward" band.
func (g *loremGen) compact(ctx context.Context, turnIdx int) error {
	// Snapshot a few of the just-written messages to carry forward verbatim
	// BEFORE we bump the generation. In the real flow these are the recent
	// messages kept out of the summary; here we synthesize an equivalent tail.
	carried := g.carriedTail(turnIdx)

	// Bump the conversation generation (mirrors IncrementConversationGeneration).
	if _, err := db.WithTxRes(g.s.db, ctx, func(q *generated.Queries) (generated.Conversation, error) {
		return q.IncrementConversationGeneration(ctx, g.convID)
	}); err != nil {
		return fmt.Errorf("increment generation: %w", err)
	}
	g.generation++
	sourceSlug := fmt.Sprintf("loremipsum-gen%d", g.generation-1)

	// in_progress status (immediately superseded by the terminal one; the UI
	// collapses the pair, but both are present as they are in real data).
	g.recordTyped(db.MessageTypeAgent, llm.Message{
		Role:                llm.MessageRoleAssistant,
		Content:             []llm.Content{{Type: llm.ContentTypeText, Text: "Distilling conversation…"}},
		ExcludedFromContext: true,
	}, llm.Usage{}, map[string]string{
		"distill_status": "in_progress",
		"source_slug":    sourceSlug,
		"new_generation": "true",
		"distill_method": "compact",
	})

	// A fresh system prompt opens the new generation (Hydrate re-adds one).
	g.recordTyped(db.MessageTypeSystem, g.systemMessage(), llm.Usage{}, nil)

	// The compaction summary, wrapped exactly as the real pi flow wraps it,
	// with the distilled=true marker so the UI renders it as a summary.
	wrapped := piCompactionSummaryPrefix + g.compactionSummary(turnIdx) + piCompactionSummarySuffix
	g.recordTyped(db.MessageTypeUser, llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: wrapped}},
	}, llm.Usage{}, map[string]string{
		"distilled":            "true",
		"distillation_content": wrapped,
		"distill_method":       "compact",
	})

	// Verbatim carried-forward tail, each stamped compaction_carried=true so
	// the UI collapses it behind a "messages carried forward" band.
	for _, c := range carried {
		mt, err := g.s.getMessageType(c)
		if err != nil {
			return err
		}
		g.recordTyped(mt, c, llm.Usage{}, map[string]string{"compaction_carried": "true"})
	}

	// Terminal complete status.
	g.recordTyped(db.MessageTypeAgent, llm.Message{
		Role:                llm.MessageRoleAssistant,
		Content:             []llm.Content{{Type: llm.ContentTypeText, Text: "Distillation complete"}},
		ExcludedFromContext: true,
	}, llm.Usage{}, map[string]string{
		"distill_status": "complete",
		"source_slug":    sourceSlug,
		"new_generation": "true",
		"distill_method": "compact",
	})

	return g.flush(ctx)
}

// turn writes one user prompt, an agent thinking+text+tool_use message, the
// tool_result message, and a closing agent message. Every Nth turn also
// injects a marker message (warning/error/modelchange/gitinfo) so all
// message types are represented. All of a turn's messages are buffered and
// committed together via flush.
func (g *loremGen) turn(ctx context.Context, i int) error {
	// User prompt.
	if err := g.record(g.userMessage(i), llm.Usage{}, nil); err != nil {
		return err
	}

	// Agent thinking + intro text + a batch of tool calls.
	toolUses := g.toolBatch(i)
	agent := llm.Message{Role: llm.MessageRoleAssistant}
	agent.Content = append(agent.Content, llm.Content{
		Type:      llm.ContentTypeThinking,
		Thinking:  g.thinkingText(i),
		Signature: "sig-" + strconv.Itoa(i),
	})
	agent.Content = append(agent.Content, llm.Content{
		Type: llm.ContentTypeText,
		Text: g.agentIntro(i),
	})
	for _, tu := range toolUses {
		agent.Content = append(agent.Content, tu.use)
	}
	if err := g.record(agent, g.usage(i, false), nil); err != nil {
		return err
	}

	// Tool results, carried in a user-role message (this is how the loop
	// records them). Display data rides along on each tool_result.
	results := llm.Message{Role: llm.MessageRoleUser}
	for _, tu := range toolUses {
		start := g.tick(1 * time.Second)
		end := g.tick(time.Duration(1+i%5) * time.Second)
		results.Content = append(results.Content, llm.Content{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        tu.use.ID,
			ToolResult:       tu.result,
			ToolError:        tu.isError,
			ToolUseStartTime: &start,
			ToolUseEndTime:   &end,
			Display:          tu.display,
		})
	}
	if err := g.record(results, llm.Usage{}, nil); err != nil {
		return err
	}

	// Occasional marker messages so every type is present.
	switch {
	case i > 0 && i%17 == 0:
		g.recordTyped(db.MessageTypeWarning, g.warningMessage(), llm.Usage{}, map[string]any{"text": g.warningText(i)})
	case i > 0 && i%23 == 0:
		if err := g.record(g.errorMessage(i), g.usage(i, false), nil); err != nil {
			return err
		}
	case i > 0 && i%31 == 0:
		g.recordTyped(db.MessageTypeModelChange, g.modelChangeMessage(i), llm.Usage{}, g.modelChangeUserData(i))
	case i > 0 && i%13 == 0:
		g.recordTyped(db.MessageTypeGitInfo, g.gitInfoMessage(), llm.Usage{}, g.gitInfoUserData())
	}

	// Closing agent message ends the turn.
	closing := llm.Message{
		Role:      llm.MessageRoleAssistant,
		Content:   []llm.Content{{Type: llm.ContentTypeText, Text: g.agentSummary(i)}},
		EndOfTurn: true,
	}
	if err := g.record(closing, g.usage(i, true), nil); err != nil {
		return err
	}
	return g.flush(ctx)
}
