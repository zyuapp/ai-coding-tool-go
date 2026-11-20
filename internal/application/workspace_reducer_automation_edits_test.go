package application

import "testing"

func TestAnAutomationsUnsavedEditsAreHeldPerThread(t *testing.T) {
	panelsPorted(t, func() {
		saved := `{"schedule":"0 8 * * *","prompt":"Check the PR","surfaceWhen":"","endsWhen":""}`
		edited, transition := panelsReduce(t, panelsWorkspace(panelsOn(panelsTask("t1"))), `{"type":"view.edit-automation","taskId":"t1","base":`+saved+`,"fields":{"schedule":"0 8 * * *","prompt":"Check the PR twice","surfaceWhen":"","endsWhen":""}}`)
		if len(transition.Effects) != 0 || edited.AutomationEdits["t1"].Fields.Prompt != "Check the PR twice" {
			t.Fatal("the edit is held")
		}
		reverted, _ := panelsReduce(t, edited, `{"type":"view.edit-automation","taskId":"t1","base":`+saved+`,"fields":`+saved+`}`)
		if len(reverted.AutomationEdits) != 0 {
			t.Error("an edit matching what is saved is let go")
		}
		if again, _ := panelsReduce(t, reverted, `{"type":"view.edit-automation","taskId":"t1","base":`+saved+`,"fields":`+saved+`}`); !sameValueOf(again.AutomationEdits, reverted.AutomationEdits) {
			t.Error("nothing to let go changes nothing")
		}
		if removed, _ := panelsReduce(t, edited, `{"type":"automation.delete","taskId":"t1"}`); len(removed.AutomationEdits) != 0 {
			t.Error("removing the automation lets the edit go")
		}
	})
}
