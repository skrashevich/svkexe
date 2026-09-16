package llm

import (
	"strings"
	"testing"
)

func TestStripInlineCitationMarkers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"none", "plain text", "plain text"},
		{
			"single group",
			"JavaScript.\ue200cite\ue202turn1search0\ue202turn1search2\ue201\nNext line.",
			"JavaScript.\nNext line.",
		},
		{
			"mid sentence",
			"gemini-2.5-flash\ue200cite\ue202turn2search0\ue201 is good.",
			"gemini-2.5-flash is good.",
		},
		{
			"stray markers no group",
			"a\ue202b\ue203c",
			"abc",
		},
		{
			"unterminated group fails open",
			"keep\ue200cite\ue202turn1search0",
			"keepciteturn1search0",
		},
		{
			// U+E203/U+E204 bracket the prose a citation covers, so the text
			// between them must survive with only the markers removed.
			"cited-span markers keep their prose",
			"\ue203A model optimized for speed.\ue204 \ue200cite\ue202turn0search3\ue201",
			"A model optimized for speed. ",
		},
		{
			"multiple groups",
			"\ue200cite\ue202turn0search0\ue201x\ue200cite\ue202turn0search1\ue201y",
			"xy",
		},
		{
			"restarted group fails the abandoned one open",
			"keep\ue200broken prose\ue200cite\ue202turn1search0\ue201after",
			"keepbroken proseafter",
		},
		{
			"over-cap group fails open",
			"a\ue200cite\ue202" + strings.Repeat("x", inlineCitationMaxBytes) + "\ue201b",
			"acite" + strings.Repeat("x", inlineCitationMaxBytes) + "b",
		},
		{
			"navlist-sized group is stripped whole",
			"before\ue200navlist\ue202" + strings.Repeat("Some Long News Headline\ue202turn0news14\ue202", 20) + "\ue201after",
			"beforeafter",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripInlineCitationMarkers(tt.in); got != tt.want {
				t.Errorf("StripInlineCitationMarkers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCitationStreamFilterAcrossDeltas(t *testing.T) {
	f := NewCitationStreamFilter()
	// Group opens in one delta and closes in a later one.
	deltas := []string{
		"Yes\ue200cite\ue202turn2search0",
		"\ue202turn2search3\ue201 it works",
	}
	var got string
	for _, d := range deltas {
		got += f.Filter(0, 0, d)
	}
	if want := "Yes it works"; got != want {
		t.Errorf("streamed = %q, want %q", got, want)
	}
}

func TestCitationStreamFilterPerIndex(t *testing.T) {
	f := NewCitationStreamFilter()
	// Output 0/content 0 opens a group; output 1/content 0 is independent.
	if got := f.Filter(0, 0, "a\ue200cite"); got != "a" {
		t.Fatalf("idx0 first = %q", got)
	}
	if got := f.Filter(1, 0, "hello"); got != "hello" {
		t.Fatalf("idx1 = %q", got)
	}
	if got := f.Filter(0, 0, "turn0search0\ue201b"); got != "b" {
		t.Fatalf("idx0 second = %q", got)
	}
}

func TestCitationStreamFilterFailsOpenAtCap(t *testing.T) {
	f := NewCitationStreamFilter()
	payload := "cite\ue202" + strings.Repeat("x", inlineCitationMaxBytes)
	got := f.Filter(0, 0, "before\ue200"+payload+" after")
	want := "beforecite" + strings.Repeat("x", inlineCitationMaxBytes) + " after"
	if got != want {
		t.Fatalf("over-cap stream = %q, want %q", got, want)
	}
}

func TestCitationStreamFilterFailsOpenAtContentDone(t *testing.T) {
	f := NewCitationStreamFilter()
	if got := f.Filter(0, 0, "before\ue200cite\ue202turn1search0"); got != "before" {
		t.Fatalf("delta = %q, want before", got)
	}
	if got := f.Finish(0, 0); got != "citeturn1search0" {
		t.Fatalf("finish = %q, want citeturn1search0", got)
	}
	if got := f.Filter(0, 0, " after"); got != " after" {
		t.Fatalf("post-finish delta = %q, want normal text", got)
	}
}
