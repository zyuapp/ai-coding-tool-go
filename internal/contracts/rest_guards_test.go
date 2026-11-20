package contracts

import (
	"errors"
	"math"
	"testing"
)

func TestErrorMessagesDropTheBridgeWrapper(t *testing.T) {
	for _, c := range []struct {
		err  any
		want string
	}{
		{errors.New("Error invoking remote method 'workspace:request': Error: No such thread"), "No such thread"},
		{errors.New("Error invoking remote method 'x': TypeError: bad"), "bad"},
		{errors.New("Error invoking remote method 'x': plain"), "plain"},
		{errors.New("No such thread"), "No such thread"},
		{"text", "text"},
		{42.0, "42"},
		{1e21, "1e+21"},
		{1e-7, "1e-7"},
		{nil, "undefined"},
		{map[string]any{}, "[object Object]"},
	} {
		if got := ErrorMessage(c.err); got != c.want {
			t.Errorf("ErrorMessage(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	if got := ReportedMessage(errors.New("Error invoking remote method 'x': y")); got != "Error invoking remote method 'x': y" {
		t.Errorf("ReportedMessage kept no wrapper: %q", got)
	}
}

func TestGlassRegionsAreBoundedFiniteAndNonNegative(t *testing.T) {
	restExpect(t, "IsGlassRegions", IsGlassRegions, restJSON(t, `[]`), true)
	restExpect(t, "IsGlassRegions", IsGlassRegions, restJSON(t, `[{"x":-4,"y":0,"width":10,"height":0,"radius":12}]`), true)
	restExpect(t, "IsGlassRegions", IsGlassRegions, restJSON(t, `[{"x":0,"y":0,"width":-1,"height":0,"radius":0}]`), false)
	restExpect(t, "IsGlassRegions", IsGlassRegions, restJSON(t, `[{"x":0,"y":0,"width":1,"height":1}]`), false)
	restExpect(t, "IsGlassRegions", IsGlassRegions, []any{map[string]any{"x": math.Inf(1), "y": 0.0, "width": 1.0, "height": 1.0, "radius": 0.0}}, false)
	restExpect(t, "IsGlassRegions", IsGlassRegions, restJSON(t, `{}`), false)
	regions := make([]any, 33)
	for i := range regions {
		regions[i] = map[string]any{"x": 0.0, "y": 0.0, "width": 1.0, "height": 1.0, "radius": 0.0}
	}
	restExpect(t, "IsGlassRegions", IsGlassRegions, regions[:32], true)
	restExpect(t, "IsGlassRegions", IsGlassRegions, regions, false)
}

func TestTerminalOutputReadsAreNullOrABoundedScreen(t *testing.T) {
	restExpect(t, "IsTerminalOutputRead", IsTerminalOutputRead, nil, true)
	restExpect(t, "IsTerminalOutputRead", IsTerminalOutputRead, restObj(t, `{"kind":"snapshot","data":"","cols":80,"rows":24,"sequence":0}`), true)
	restExpect(t, "IsTerminalOutputRead", IsTerminalOutputRead, restObj(t, `{"kind":"output","data":"x","cols":1000,"rows":1,"sequence":7}`), true)
	restExpect(t, "IsTerminalOutputRead", IsTerminalOutputRead, restObj(t, `{"kind":"output","data":"x","cols":1001,"rows":1,"sequence":7}`), false)
	restExpect(t, "IsTerminalOutputRead", IsTerminalOutputRead, restObj(t, `{"kind":"output","data":"x","cols":80.5,"rows":1,"sequence":7}`), false)
	restExpect(t, "IsTerminalOutputRead", IsTerminalOutputRead, restObj(t, `{"kind":"output","data":"x","cols":80,"rows":1,"sequence":-1}`), false)
	restExpect(t, "IsTerminalOutputRead", IsTerminalOutputRead, restObj(t, `{"kind":"diff","data":"x","cols":80,"rows":1,"sequence":1}`), false)
	restExpect(t, "IsTerminalDimension", IsTerminalDimension, 0.0, false)
	restExpect(t, "IsTerminalDimension", IsTerminalDimension, 1.0, true)
}

func TestSessionResourceMessagesNameTheirThreadAndSession(t *testing.T) {
	restExpect(t, "IsSessionResourceMessage", IsSessionResourceMessage, restObj(t, `{"type":"session.process","taskId":"t","sessionId":"s","pid":42}`), true)
	restExpect(t, "IsSessionResourceMessage", IsSessionResourceMessage, restObj(t, `{"type":"session.process","taskId":"t","sessionId":"s","pid":1}`), false)
	restExpect(t, "IsSessionResourceMessage", IsSessionResourceMessage, restObj(t, `{"type":"session.released","taskId":"t","sessionId":"s"}`), true)
	restExpect(t, "IsSessionResourceMessage", IsSessionResourceMessage, restObj(t, `{"type":"session.state","taskId":"t","sessionId":"s","busy":false}`), true)
	restExpect(t, "IsSessionResourceMessage", IsSessionResourceMessage, restObj(t, `{"type":"session.state","taskId":"t","sessionId":"","busy":false}`), false)
	restExpect(t, "IsSessionResourceMessage", IsSessionResourceMessage, restObj(t, `{"type":"session.released","taskId":"`+restRepeat("t", 257)+`","sessionId":"s"}`), false)
	restExpect(t, "IsTerminateSessionCommand", IsTerminateSessionCommand, restObj(t, `{"type":"terminate-session","taskId":"t","sessionId":"s","idleOnly":true}`), true)
	restExpect(t, "IsTerminateSessionCommand", IsTerminateSessionCommand, restObj(t, `{"type":"terminate-session","taskId":"t","sessionId":"s","idleOnly":false}`), false)
	restExpect(t, "IsResourceStoppedEvent", IsResourceStoppedEvent, restObj(t, `{"type":"resource.stopped","taskId":"t"}`), false)
	restExpect(t, "IsResourceStoppedEvent", IsResourceStoppedEvent, restObj(t, `{"type":"resource.stopped","taskId":"t","stop":{"at":1,"bytes":2048,"cleanup":"complete"}}`), true)
}

func TestPairedThreadResultsAreCheckedBeforeFormatting(t *testing.T) {
	summary := restObj(t, `{"id":"t1","title":"","status":"idle","archived":false,"createdAt":1,"lastActivityAt":2,"messageCount":0,"attachmentCount":3,"role":"reviewer","projectId":"p"}`)
	restExpect(t, "IsThreadSummary", IsThreadSummary, summary, true)
	for _, broken := range []map[string]any{
		restWith(t, summary, "id", ""),
		restWith(t, summary, "status", "paused"),
		restWith(t, summary, "pausedUntil", "soon"),
		restWith(t, summary, "messageCount", 1.5),
		restWith(t, summary, "role", "manager"),
		restWith(t, summary, "worktreeRoot", nil),
	} {
		restExpect(t, "IsThreadSummary", IsThreadSummary, broken, false)
	}
	transcript := map[string]any{"thread": summary, "omitted": 0.0, "messages": restJSON(t, `[{"kind":"user","text":"hi","at":1},{"kind":"tool","text":"","origin":"Mac","at":2}]`)}
	restExpect(t, "IsThreadTranscript", IsThreadTranscript, transcript, true)
	restExpect(t, "IsThreadTranscript", IsThreadTranscript, restWith(t, transcript, "messages", restJSON(t, `[{"kind":"user","text":"`+restRepeat("x", 2_002)+`","at":1}]`)), false)
	restExpect(t, "IsThreadTranscript", IsThreadTranscript, restWith(t, transcript, "messages", restJSON(t, `[{"kind":"note","text":"hi","at":1}]`)), false)
	restExpect(t, "IsThreadTranscript", IsThreadTranscript, restWith(t, transcript, "omitted", -1), false)
}
