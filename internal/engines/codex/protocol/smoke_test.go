package protocol

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestSmokeRealCodex drives the installed Codex app server when CODEX_SMOKE=1: the handshake, the
// account, and the skills of a folder. It starts no turn.
func TestSmokeRealCodex(t *testing.T) {
	if os.Getenv("CODEX_SMOKE") == "" {
		t.Skip("set CODEX_SMOKE=1 to run against the installed codex")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := Connect(Command{Executable: executable, Args: []string{"app-server", "--listen", "stdio://"}})
	defer func() { t.Logf("closed: %+v", client.Close()) }()
	server, err := client.Initialize(ctx, DefaultClientInfo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("initialize: %+v", *server)
	account, err := Call[GetAccountResponse](ctx, client, "account/read", GetAccountParams{RefreshToken: new(bool)})
	if err != nil {
		t.Fatal(err)
	}
	if account.Account != nil {
		t.Logf("account: %s", account.Account.Type)
	} else {
		t.Log("account: signed out")
	}
	skills, err := Call[SkillsListResponse](ctx, client, "skills/list", SkillsListParams{Cwds: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(skills.Data) != 1 {
		t.Fatalf("skills: %+v", skills)
	}
	t.Logf("skills: %d", len(skills.Data[0].Skills))
	cwd, ephemeral, sandbox := t.TempDir(), true, SandboxReadOnly
	started, err := Call[ThreadSessionResponse](ctx, client, "thread/start", ThreadStartParams{Cwd: &cwd, Ephemeral: &ephemeral, Sandbox: &sandbox, ApprovalPolicy: ApprovalMode(ApprovalNever)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("thread: %s model %s status %s policy %+v", started.Thread.ID, started.Model, started.Thread.Status.Type, started.ApprovalPolicy)
}
