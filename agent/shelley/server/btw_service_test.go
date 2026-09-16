package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

type btwCapturingService struct {
	llm.Service
	mu       sync.Mutex
	requests []*llm.Request
	inputs   []*llm.Request
}

func (s *btwCapturingService) Do(_ context.Context, request *llm.Request) (*llm.Response, error) {
	copyRequest := *request
	copyRequest.System = append([]llm.SystemContent(nil), request.System...)
	s.mu.Lock()
	s.requests = append(s.requests, &copyRequest)
	s.inputs = append(s.inputs, request)
	s.mu.Unlock()
	return &llm.Response{}, nil
}

func (s *btwCapturingService) lastRequest() *llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1]
}

func TestBtwServiceFrozenPrefixIsStableAndRequestIsImmutable(t *testing.T) {
	_, database, _ := newTestServer(t)
	ctx := context.Background()
	parent, err := database.CreateConversation(ctx, nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	create := func(messageType db.MessageType, message llm.Message) int64 {
		t.Helper()
		row, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: parent.ConversationID,
			Type:           messageType,
			LLMData:        message,
		})
		if err != nil {
			t.Fatal(err)
		}
		return row.SequenceID
	}
	create(db.MessageTypeSystem, llm.UserStringMessage("PARENT_SYSTEM"))
	started := time.Date(2026, 8, 31, 12, 34, 56, 0, time.UTC)
	create(db.MessageTypeAgent, llm.Message{
		Role: llm.MessageRoleAssistant,
		Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: "paired-tool", ToolName: "bash",
			ToolInput: json.RawMessage(`{"command":"printf paired"}`), ToolUseStartTime: &started,
		}},
	})
	rawImage := strings.Repeat("RAW_BASE64_IMAGE_DATA", 20_000)
	create(db.MessageTypeTool, llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: "paired-tool",
			ToolResult: append(llm.TextContent("PAIRED_RESULT"), llm.Content{
				Type: llm.ContentTypeText, MediaType: "image/png", Data: rawImage,
			}),
		}},
	})
	cutoff := create(db.MessageTypeUser, llm.UserStringMessage("FROZEN_AT_POINTER"))
	pointer := db.BtwParentPointer{Generation: parent.CurrentGeneration, SequenceID: cutoff}

	capture := &btwCapturingService{Service: predictable.NewService()}
	decorate := func(pointer db.BtwParentPointer) llm.Service {
		t.Helper()
		service, err := newBtwService(ctx, database, parent.ConversationID, pointer, 2, capture)
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	service := decorate(pointer)
	if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: parent.ConversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.UserStringMessage("LATER_PARENT_TEXT"),
	}); err != nil {
		t.Fatal(err)
	}

	original := &llm.Request{
		System:        []llm.SystemContent{{Type: "text", Text: "READER_RESTRICTION"}},
		Messages:      []llm.Message{llm.UserStringMessage("CHILD_QUESTION")},
		Tools:         []*llm.Tool{{Name: "bash"}},
		ThinkingLevel: llm.ThinkingLevelHigh,
	}
	before, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := service.Do(ctx, original); err != nil {
			t.Fatal(err)
		}
	}
	after, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("decorator mutated request:\nbefore=%s\nafter=%s", before, after)
	}
	request := capture.lastRequest()
	if len(request.System) != 2 || request.System[1].Type != "text" || !request.System[1].Cache {
		t.Fatalf("frozen system content = %#v", request.System)
	}
	frozen := request.System[1].Text
	for _, text := range []string{
		btwFrozenReferenceBegin, "PARENT_SYSTEM", "paired-tool", "PAIRED_RESULT",
		"FROZEN_AT_POINTER", btwFrozenReferenceEnd,
	} {
		if !strings.Contains(frozen, text) {
			t.Fatalf("frozen prefix missing %q: %s", text, frozen)
		}
	}
	if strings.Contains(frozen, "LATER_PARENT_TEXT") || strings.Contains(frozen, "CHILD_QUESTION") ||
		strings.Contains(frozen, "2026-08-31T12:34:56") || strings.Contains(frozen, rawImage) {
		t.Fatalf("frozen prefix contains mutable/later text: %s", frozen)
	}
	if !strings.Contains(frozen, btwBinaryDataPlaceholder) || len(frozen) >= len(rawImage) {
		t.Fatalf("frozen binary normalization is missing or unbounded: frozen=%d raw=%d", len(frozen), len(rawImage))
	}
	if strings.Index(frozen, "paired-tool") >= strings.Index(frozen, "PAIRED_RESULT") {
		t.Fatalf("binary normalization changed tool pair ordering: %s", frozen)
	}
	first := capture.requests[0].System[1]
	if !reflect.DeepEqual(first, request.System[1]) {
		t.Fatalf("repeated prefix changed:\nfirst=%#v\nlast=%#v", first, request.System[1])
	}
	var calls sync.WaitGroup
	for range 8 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			if _, err := service.Do(ctx, original); err != nil {
				t.Errorf("concurrent request: %v", err)
			}
		}()
	}
	calls.Wait()
	for _, captured := range capture.requests {
		if captured.System[1] != first {
			t.Fatal("concurrent request changed frozen prefix")
		}
	}

	frozenFor := func(pointer db.BtwParentPointer) string {
		t.Helper()
		if _, err := decorate(pointer).Do(ctx, original); err != nil {
			t.Fatal(err)
		}
		return capture.lastRequest().System[1].Text
	}
	for _, name := range []string{"recreated", "sibling"} {
		t.Run(name, func(t *testing.T) {
			if got := frozenFor(pointer); got != frozen {
				t.Fatal("same parent pointer did not recreate byte-identical prefix")
			}
		})
	}

	later, err := database.ListMessages(ctx, parent.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if changed := frozenFor(db.BtwParentPointer{
		Generation: parent.CurrentGeneration,
		SequenceID: later[len(later)-1].SequenceID,
	}); changed == frozen {
		t.Fatal("changed pointer produced identical prefix")
	}
}

type btwCapabilityService struct {
	llm.Service
}

func (btwCapabilityService) PatchProfile() string { return "codex_apply_patch" }
func (btwCapabilityService) SupportsReasoning() bool {
	return false
}

func (btwCapabilityService) SupportedReasoningLevels() []llm.ThinkingLevel {
	return []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelHigh}
}
func (btwCapabilityService) DefaultReasoningLevel() string     { return "high" }
func (btwCapabilityService) SupportsServerSideWebSearch() bool { return true }

func TestBtwServiceForwardsOptionalCapabilities(t *testing.T) {
	inner := btwCapabilityService{Service: predictable.NewService()}
	service := &btwService{Service: inner}
	if service.Provider() != inner.Provider() ||
		service.TokenContextWindow() != inner.TokenContextWindow() ||
		service.MaxImageDimension() != inner.MaxImageDimension() ||
		service.MaxImageBytes() != inner.MaxImageBytes() ||
		service.SupportsImages() != inner.SupportsImages() {
		t.Fatal("core service capability was not forwarded")
	}
	if service.PatchProfile() != "codex_apply_patch" || service.SupportsReasoning() ||
		!reflect.DeepEqual(service.SupportedReasoningLevels(), []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelHigh}) ||
		service.DefaultReasoningLevel() != "high" || !service.SupportsServerSideWebSearch() {
		t.Fatal("optional service capability was not forwarded")
	}
}

func TestConversationManagerNilDecoratorPreservesServiceAndRequest(t *testing.T) {
	inner := &btwCapturingService{Service: predictable.NewService()}
	manager := &ConversationManager{}
	service, err := manager.serviceForLoop(inner)
	if err != nil || service != inner {
		t.Fatalf("nil decorator changed service: got=%T err=%v", service, err)
	}
	request := &llm.Request{
		System:   []llm.SystemContent{{Type: "text", Text: "unchanged"}},
		Messages: []llm.Message{llm.UserStringMessage("ordinary")},
	}
	if _, err := service.Do(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if inner.inputs[0] != request {
		t.Fatal("nil decorator replaced request pointer")
	}
	if got := inner.lastRequest(); got.System[0] != request.System[0] ||
		!reflect.DeepEqual(got.Messages, request.Messages) {
		t.Fatalf("nil decorator changed request: got=%#v want=%#v", got, request)
	}
}

func TestLimitBtwFrozenHistoryNeverOrphansTools(t *testing.T) {
	item := func(contents ...llm.Content) btwFrozenMessage {
		return btwFrozenMessage{message: llm.Message{Content: contents}}
	}
	use := func(id string) llm.Content {
		return llm.Content{Type: llm.ContentTypeToolUse, ID: id, ToolName: "bash"}
	}
	result := func(id string) llm.Content {
		return llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: id}
	}
	text := func(value string) llm.Content {
		return llm.Content{Type: llm.ContentTypeText, Text: value}
	}
	assertPaired := func(t *testing.T, history []btwFrozenMessage) {
		t.Helper()
		uses := map[string]int{}
		results := map[string]int{}
		for _, message := range history {
			for _, content := range message.message.Content {
				switch content.Type {
				case llm.ContentTypeToolUse:
					uses[content.ID]++
				case llm.ContentTypeToolResult:
					results[content.ToolUseID]++
				}
			}
		}
		if !reflect.DeepEqual(uses, results) {
			t.Fatalf("tool pairs differ: uses=%v results=%v history=%#v", uses, results, history)
		}
	}

	pair := []btwFrozenMessage{item(use("a")), item(result("a"))}
	deterministic := []btwFrozenMessage{
		item(use("a"), use("dangling")), item(result("a")), item(text("tail")),
	}
	transitive := []btwFrozenMessage{
		item(use("a")), item(result("a"), use("b")), item(result("b")),
	}
	tests := []struct {
		name          string
		history, want []btwFrozenMessage
		limit         int
		repeat        bool
	}{
		{"result pulls use across truncation", pair, pair, 1, false},
		{
			"dangling use at frozen cutoff is removed",
			[]btwFrozenMessage{item(text("kept"), use("dangling"))},
			[]btwFrozenMessage{item(text("kept"))},
			30, false,
		},
		{
			"orphan result at truncation is removed",
			[]btwFrozenMessage{item(result("missing"))},
			[]btwFrozenMessage{},
			1, false,
		},
		{
			"pair closure is deterministic", deterministic,
			[]btwFrozenMessage{item(use("a")), item(result("a")), item(text("tail"))},
			2, true,
		},
		{"pair closure is transitive", transitive, transitive, 1, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := limitBtwFrozenHistory(test.history, test.limit)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("history=%#v want=%#v", got, test.want)
			}
			if test.repeat && !reflect.DeepEqual(got, limitBtwFrozenHistory(test.history, test.limit)) {
				t.Fatal("repeated truncation changed history")
			}
			assertPaired(t, got)
		})
	}
}
