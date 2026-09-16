package server

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

// TestComputeListPatchFuzz generates random old/new list pairs and verifies
// the emitted patch turns oldList into newList. Catches index-math regressions
// triggered by complex reorderings (the "bad array index in patch path"
// crash class seen by the UI client).
//
// The trials are split into parallel shards, each with its own deterministic
// seed: single-threaded 2000 trials ran 35-45s under -race and was the
// longest test in the package (it gated the whole CI shard). 8x125 keeps the
// coverage class (complex random reorderings) at a fraction of the cost, and
// still fully deterministic.
func TestComputeListPatchFuzz(t *testing.T) {
	t.Parallel()
	const (
		shards         = 8
		trialsPerShard = 125
	)
	for shard := 0; shard < shards; shard++ {
		t.Run(fmt.Sprintf("shard%d", shard), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(int64(shard + 1)))
			for trial := 0; trial < trialsPerShard; trial++ {
				oldList := randomList(rng)
				newList := mutate(rng, oldList)
				ops, err := computeListPatch(oldList, newList)
				if err != nil {
					t.Fatalf("trial %d: compute: %v", trial, err)
				}
				got, err := applyTestPatch(oldList, ops)
				if err != nil {
					t.Fatalf("trial %d: apply: %v\nold=%s\nnew=%s\nops=%s", trial, err,
						dumpList(oldList), dumpList(newList), dumpOps(ops))
				}
				if !equalLists(got, newList) {
					t.Fatalf("trial %d: mismatch\nold=%s\nnew=%s\ngot=%s\nops=%s",
						trial, dumpList(oldList), dumpList(newList), dumpList(got), dumpOps(ops))
				}
			}
		})
	}
}

func randomList(rng *rand.Rand) []ConversationWithState {
	n := rng.Intn(80)
	out := make([]ConversationWithState, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, mkRandConv(rng))
	}
	return out
}

func mkRandConv(rng *rand.Rand) ConversationWithState {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	slug := "s"
	c := ConversationWithState{
		Conversation: generated.Conversation{
			ConversationID:      randID(rng),
			Slug:                &slug,
			UserInitiated:       true,
			CreatedAt:           now,
			UpdatedAt:           now,
			ConversationOptions: "{}",
		},
		Working: rng.Intn(2) == 0,
	}
	if rng.Intn(2) == 0 {
		c.GitWorktreeRoot = "/tmp/x"
	}
	return c
}

func randID(rng *rand.Rand) string {
	return fmt.Sprintf("id-%d", rng.Intn(120))
}

func mutate(rng *rand.Rand, in []ConversationWithState) []ConversationWithState {
	out := append([]ConversationWithState(nil), in...)
	steps := rng.Intn(20)
	for i := 0; i < steps; i++ {
		op := rng.Intn(4)
		switch op {
		case 0: // add
			out = append(out, ConversationWithState{})
			idx := rng.Intn(len(out))
			copy(out[idx+1:], out[idx:])
			out[idx] = mkRandConv(rng)
		case 1: // remove
			if len(out) == 0 {
				continue
			}
			idx := rng.Intn(len(out))
			out = append(out[:idx], out[idx+1:]...)
		case 2: // mutate field
			if len(out) == 0 {
				continue
			}
			idx := rng.Intn(len(out))
			out[idx].Working = !out[idx].Working
		case 3: // move
			if len(out) < 2 {
				continue
			}
			i := rng.Intn(len(out))
			j := rng.Intn(len(out))
			if i == j {
				continue
			}
			item := out[i]
			out = append(out[:i], out[i+1:]...)
			out = append(out, ConversationWithState{})
			copy(out[j+1:], out[j:])
			out[j] = item
		}
	}
	// Deduplicate by ID (the diff requires unique IDs).
	seen := map[string]bool{}
	dedup := out[:0]
	for _, c := range out {
		if c.ConversationID == "" || seen[c.ConversationID] {
			continue
		}
		seen[c.ConversationID] = true
		dedup = append(dedup, c)
	}
	return dedup
}

func dumpList(l []ConversationWithState) string {
	b, _ := json.Marshal(l)
	return string(b)
}

func dumpOps(ops []conversationListPatchOp) string {
	b, _ := json.Marshal(ops)
	return string(b)
}

func equalLists(a, b []ConversationWithState) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return dumpList(a) == dumpList(b)
}

// FuzzComputeListPatchSeq drives a long sequence of mutations and verifies
// each emitted patch is applicable to the prior state, mirroring how the
// client consumes the stream.
func FuzzComputeListPatchSeq(f *testing.F) {
	f.Add(int64(1), uint32(50))
	f.Add(int64(2), uint32(200))
	f.Fuzz(func(t *testing.T, seed int64, steps uint32) {
		if steps > 500 {
			steps = 500
		}
		rng := rand.New(rand.NewSource(seed))
		serverState := []ConversationWithState{}
		clientState := []ConversationWithState{}
		for i := uint32(0); i < steps; i++ {
			next := mutate(rng, serverState)
			ops, err := computeListPatch(serverState, next)
			if err != nil {
				t.Fatalf("step %d compute: %v", i, err)
			}
			applied, err := applyTestPatch(clientState, ops)
			if err != nil {
				t.Fatalf("step %d apply: %v\nserver=%s\nnext=%s\nops=%s", i, err,
					dumpList(serverState), dumpList(next), dumpOps(ops))
			}
			if !equalLists(applied, next) {
				t.Fatalf("step %d mismatch\nserver=%s\nnext=%s\napplied=%s\nops=%s",
					i, dumpList(serverState), dumpList(next), dumpList(applied), dumpOps(ops))
			}
			serverState = next
			clientState = applied
		}
	})
}
