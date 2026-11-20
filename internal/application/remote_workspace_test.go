package application

import (
	"encoding/json/jsontext"
	"maps"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// A record with `fields` laid over it, as `{ ...record, ...fields }` makes one.
func cmpSpread(record map[string]any, fields map[string]any) map[string]any {
	next := maps.Clone(record)
	maps.Copy(next, fields)
	return next
}

func cmpWireTask(id string, edits ...func(*domain.Thread)) map[string]any {
	return remoteWireForm(cmpTask(id, edits...)).(map[string]any)
}

func cmpPatch(t *testing.T, text string) *contracts.WorkspacePatch {
	t.Helper()
	patch := &contracts.WorkspacePatch{}
	if err := jsonx.Unmarshal([]byte(text), patch); err != nil {
		t.Fatalf("patch %s: %v", text, err)
	}
	return patch
}

func TestOlderSnapshotsAcquireDefaultsWithoutChangingTheirWire(t *testing.T) {
	cmpPorted(t, func() {
		read := CreateRemoteWorkspaceReader()
		wire := map[string]any{
			"threads": []any{cmpWireTask("old")}, "projects": []any{}, "currentId": "old",
			"docks": map[string]any{"old": map[string]any{"open": true, "panels": []any{}, "tab": "home"}},
		}
		first, err := read(wire)
		if err != nil {
			t.Fatal(err)
		}
		if first.CurrentID == nil || *first.CurrentID != "old" || first.Threads[0].ID != "old" {
			t.Error("the thread is read")
		}
		if first.SideChats == nil || len(first.SideChats) != 0 || first.Goals == nil || len(first.Goals) != 0 {
			t.Errorf("defaults: %v %v", first.SideChats, first.Goals)
		}
		if terminals := first.Docks["old"].Terminals; terminals == nil || len(terminals) != 0 {
			t.Error("a dock gets its defaults")
		}
		if _, ok := wire["goals"]; ok {
			t.Error("the wire is left as it came")
		}
		patched, err := ApplyWorkspacePatches(wire, []*contracts.WorkspacePatch{
			cmpPatch(t, `{"path":["docks","old","terminals"],"value":[]}`),
			cmpPatch(t, `{"path":["threads"],"splice":{"index":1,"deleteCount":0,"items":[`+string(mustJSON(t, cmpTask("next")))+`]}}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		second, err := read(patched)
		if err != nil {
			t.Fatal(err)
		}
		if len(second.Threads) != 2 || second.Threads[1].ID != "next" {
			t.Error("the spliced thread is read")
		}
		if !jsonx.SameSlice(second.SideChats, first.SideChats) || !jsonx.SameMap(second.Goals, first.Goals) {
			t.Error("unchanged defaults keep their identity")
		}
		removedWire, err := ApplyWorkspacePatches(patched, []*contracts.WorkspacePatch{cmpPatch(t, `{"path":["docks","old","terminals"],"remove":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		removed, err := read(removedWire)
		if err != nil {
			t.Fatal(err)
		}
		if terminals := removed.Docks["old"].Terminals; terminals == nil || len(terminals) != 0 {
			t.Error("a removed field reads as its default")
		}
	})
}

func mustJSON(t *testing.T, value any) jsontext.Value {
	t.Helper()
	data, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUnchangedBranchesKeepTheirIdentity(t *testing.T) {
	cmpPorted(t, func() {
		read := CreateRemoteWorkspaceReader()
		empty := remoteWireForm(EmptyWorkspaceState(nil)).(map[string]any)
		wire := cmpSpread(empty, map[string]any{"future": map[string]any{"enabled": true}, "threads": []any{cmpWireTask("t1")}})
		first, err := read(wire)
		if err != nil {
			t.Fatal(err)
		}
		if again, _ := read(wire); again != first {
			t.Error("the same wire reads as the same state")
		}
		moved, err := read(cmpSpread(wire, map[string]any{"composerFocus": 2.0}))
		if err != nil {
			t.Fatal(err)
		}
		if moved.ComposerFocus != 2 || !jsonx.SameSlice(moved.Threads, first.Threads) || moved.Threads[0] != first.Threads[0] || !jsonx.SameMap(moved.Docks, first.Docks) {
			t.Error("unchanged branches keep their identity")
		}
		if _, err := read(cmpSpread(wire, map[string]any{"threads": nil})); err == nil || !strings.Contains(err.Error(), "collection") {
			t.Errorf("a list that is not one: %v", err)
		}
		if _, err := read([]any{}); err == nil {
			t.Error("a workspace that is not a record")
		}
	})
}

func TestOlderCollectionRecordsGetRequiredDefaults(t *testing.T) {
	cmpPorted(t, func() {
		read := CreateRemoteWorkspaceReader()
		current := cmpWireTask("current")
		old := map[string]any{"id": "old", "engine": "claude", "messages": []any{}}
		wire := map[string]any{
			"threads":   []any{old, current},
			"projects":  []any{map[string]any{"id": "project", "root": "/repo"}},
			"sideChats": []any{map[string]any{"id": "side", "sourceThreadId": "old"}},
		}
		normalized, err := read(wire)
		if err != nil {
			t.Fatal(err)
		}
		if normalized.Threads[0].ExecutionPolicy != domain.ExecutionPolicyConfirm || normalized.Threads[0].Title != "Untitled thread" {
			t.Errorf("thread defaults: %+v", normalized.Threads[0].ThreadCore)
		}
		cmpSameJSON(t, normalized.Threads[0].LastChangeSnapshot, domain.ChangeSnapshot{Files: []string{}, CapturedAt: 0}, "snapshot")
		if normalized.SideChats[0].Error != nil {
			t.Error("side chat error defaults to null")
		}
		if again, _ := read(wire); again != normalized {
			t.Error("the same wire reads as the same state")
		}
		if moved, _ := read(cmpSpread(wire, map[string]any{"composerFocus": 1.0})); !jsonx.SameSlice(moved.Threads, normalized.Threads) {
			t.Error("a filled list keeps its identity while it does not move")
		}
		if _, ok := old["executionPolicy"]; ok {
			t.Error("the wire record is left as it came")
		}
		if _, err := read(map[string]any{"threads": []any{map[string]any{"title": "missing identity"}}}); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Errorf("missing identity: %v", err)
		}
	})
}

func TestANewerBuildsThreadIsKeptAsSent(t *testing.T) {
	cmpPorted(t, func() {
		read := CreateRemoteWorkspaceReader()
		proposal := func(id string, extra map[string]any) map[string]any {
			return cmpSpread(map[string]any{"id": id, "title": "S " + id, "prompt": "Do it", "place": map[string]any{"worktree": false}, "proposedAt": 1.0}, extra)
		}
		known := cmpWireTask("known", func(thread *domain.Thread) {
			thread.Role, thread.Model, thread.Effort = new(domain.ThreadRoleCoordinator), new("opus"), new(domain.AgentEffortHigh)
		})
		newer := cmpSpread(cmpWireTask("newer"), map[string]any{
			"role": "designer", "model": "opus-next", "effort": "galaxy", "outcome": "celebrated", "executionPolicy": "yolo",
			"threadModel": map[string]any{"model": "opus-next"},
			"proposals":   []any{proposal("p1", nil), proposal("p2", map[string]any{"model": "opus-next"}), proposal("p3", map[string]any{"model": "gemini-next", "engine": "gemini"}), proposal("p4", map[string]any{"model": "opus", "engine": 7.0})},
		})
		elsewhere := cmpSpread(cmpWireTask("elsewhere"), map[string]any{"engine": "gemini"})
		wire := cmpSpread(remoteWireForm(EmptyWorkspaceState(nil)).(map[string]any), map[string]any{"threads": []any{known, newer, elsewhere}})
		state, err := read(wire)
		if err != nil {
			t.Fatal(err)
		}
		threads := state.Threads
		if ids := []string{threads[0].ID, threads[1].ID, threads[2].ID}; strings.Join(ids, ",") != "known,newer,elsewhere" {
			t.Fatalf("ids = %v", ids)
		}
		read2 := threads[1]
		if read2.Role != nil || read2.Outcome != nil {
			t.Error("a role or verdict this build has no mark for is left out")
		}
		if *read2.Model != "opus-next" || *read2.Effort != "galaxy" || read2.ExecutionPolicy != "yolo" || read2.ThreadModel == nil || read2.ThreadModel.Model != "opus-next" {
			t.Error("what it runs on is kept as sent")
		}
		var proposals []string
		for _, item := range read2.Proposals {
			proposals = append(proposals, item.ID)
		}
		if strings.Join(proposals, ",") != "p1,p2,p3" || *read2.Proposals[2].Engine != "gemini" {
			t.Errorf("proposals = %v", proposals)
		}
		if threads[2].Engine != "gemini" {
			t.Error("an engine this build does not know is kept")
		}
		if again, _ := read(maps.Clone(wire)); !jsonx.SameSlice(again.Threads, threads) {
			t.Error("the list is read once while it does not move")
		}
	})
}

func TestTheReplicaAppliesPatchesInOrderAndAsksForASnapshotOtherwise(t *testing.T) {
	cmpPorted(t, func() {
		replica := CreateRemoteWorkspaceReplica()
		if state, resync, err := replica(&contracts.ComputerWorkspaceUpdate{Revision: 1, Patches: []*contracts.WorkspacePatch{}}); state != nil || !resync || err != nil {
			t.Error("a patch with nothing held asks for a snapshot")
		}
		snapshot := mustJSON(t, map[string]any{"threads": []any{cmpWireTask("t1")}})
		state, resync, err := replica(&contracts.ComputerWorkspaceUpdate{Revision: 3, State: snapshot})
		if err != nil || resync || state == nil || state.Threads[0].ID != "t1" {
			t.Fatalf("snapshot: %v %v %v", state, resync, err)
		}
		next, _, err := replica(&contracts.ComputerWorkspaceUpdate{Revision: 4, Patches: []*contracts.WorkspacePatch{cmpPatch(t, `{"path":["composerFocus"],"value":5}`)}})
		if err != nil || next == nil || next.ComposerFocus != 5 {
			t.Fatalf("patch: %v %v", next, err)
		}
		if stale, resync, _ := replica(&contracts.ComputerWorkspaceUpdate{Revision: 4, Patches: []*contracts.WorkspacePatch{}}); stale != nil || resync {
			t.Error("a stale update is ignored")
		}
		if skipped, resync, _ := replica(&contracts.ComputerWorkspaceUpdate{Revision: 6, Patches: []*contracts.WorkspacePatch{}}); skipped != nil || !resync {
			t.Error("a skipped revision asks for a snapshot")
		}
		if _, _, err := replica(&contracts.ComputerWorkspaceUpdate{Revision: 7, State: mustJSON(t, map[string]any{"threads": "no"})}); err == nil {
			t.Error("an unreadable snapshot is an error")
		}
	})
}
