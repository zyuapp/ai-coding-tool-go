package domain

import "testing"

func TestWorktreeHueIsDeterministicWithinThePalette(t *testing.T) {
	for _, id := range []string{"a1b2c3d4", "0f9e8d7c", "deadbeef", "", "ai-coding-tool-a1b2c3d4"} {
		if hue := WorktreeHue(id); hue < 0 || hue >= WorktreeHues || hue != WorktreeHue(id) {
			t.Fatalf("%s gave %d", id, hue)
		}
	}
	// Math.imul(hash, 31) + code | 0 over "deadbeef" and a long id, as JavaScript computes them.
	if WorktreeHue("deadbeef") != 0 || WorktreeHue("ai-coding-tool-a1b2c3d4") != 1 {
		t.Fatal(WorktreeHue("deadbeef"), WorktreeHue("ai-coding-tool-a1b2c3d4"))
	}
}

func TestWorktreeNames(t *testing.T) {
	if got := WorktreeDirectoryName("/Users/me/Projects/AI Coding Tool/", "a1b2c3d4"); got != "ai-coding-tool-a1b2c3d4" {
		t.Fatal(got)
	}
	if got := WorktreeDirectoryName("/", "a1b2c3d4"); got != "project-a1b2c3d4" {
		t.Fatal(got)
	}
	if got := WorktreeDirectoryName("/x/-Über Långt namn som är väldigt långt-", "id"); got != "ber-l-ngt-namn-som-r-v-l-id" {
		t.Fatal(got)
	}
	if WorktreeIDFromDirectoryName("ai-coding-tool-a1b2c3d4") != "a1b2c3d4" || WorktreeIDFromDirectoryName("my tree!") != "my-tree-" {
		t.Fatal("ids from names")
	}
	if got := SnapshotMessage("Fix it", "t-1", WorktreeReleaseDeleted, "refs/aicodingtool/a"); got != "aicodingtool: snapshot \"Fix it\"\n\nThread t-1 · deleted manually\nRef refs/aicodingtool/a" {
		t.Fatal(got)
	}
	if got := SnapshotMessage("Fix it", "", WorktreeReleaseReturnedToLocal, ""); got != "aicodingtool: snapshot \"Fix it\"\n\nNo thread · returned to local" {
		t.Fatal(got)
	}
	if WorktreeName(&Worktree{ID: "w", Root: "/a/b/"}) != "b" || WorktreeName(&Worktree{ID: "w", Root: "/"}) != "w" {
		t.Fatal("names")
	}
	valid := map[string]any{"id": "w", "projectId": "p", "root": "/r", "workspaceId": "ws", "baseCommit": "abc", "createdAt": 1.0, "lastUsedAt": 2.0}
	if !IsWorktree(valid) {
		t.Fatal("valid worktree")
	}
	valid["name"] = " "
	if IsWorktree(valid) {
		t.Fatal("blank name")
	}
}
