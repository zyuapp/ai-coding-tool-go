package terminal

import (
	"encoding/json/v2"
	"testing"
)

func TestWireShapesMatchTheTypeScriptContracts(t *testing.T) {
	code, status := 3.0, StatusExited
	for _, tc := range []struct {
		value any
		want  string
	}{
		{DataEvent{TerminalID: "t", Data: "x", Sequence: 2}, `{"terminalId":"t","data":"x","sequence":2}`},
		{DataEvent{TerminalID: "t", Sequence: 3, Size: &Size{Cols: 80, Rows: 24}}, `{"terminalId":"t","data":"","sequence":3,"size":{"cols":80,"rows":24}}`},
		{OutputRead{TerminalScreenSnapshot: ScreenSnapshot{Data: "d", Cols: 80, Rows: 24, Sequence: 1}, Kind: ReadOutput}, `{"data":"d","cols":80,"rows":24,"sequence":1,"kind":"output"}`},
		{Text{Omitted: 0}, `{"lines":[],"omitted":0}`},
		{Update{TerminalID: "t", Status: &status, ExitCode: &code}, `{"terminalId":"t","status":"exited","exitCode":3}`},
	} {
		data, err := json.Marshal(tc.value)
		if err != nil || string(data) != tc.want {
			t.Errorf("%T encoded %s (%v), want %s", tc.value, data, err, tc.want)
		}
	}
}
