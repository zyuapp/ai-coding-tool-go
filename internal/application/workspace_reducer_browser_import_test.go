package application

import (
	"slices"
	"testing"
)

const (
	panelsBrave   = `{"id":"brave:0:Default","browser":"Brave","profile":"Personal"}`
	panelsFirefox = `{"id":"firefox:0:Profiles/a","browser":"Firefox","profile":"default"}`
)

func panelsSourcesListed(t *testing.T) *WorkspaceState {
	return panelsRun(t, panelsWorkspace(), `{"type":"browser-import.sources","sources":[`+panelsBrave+`,`+panelsFirefox+`]}`)
}

func TestSettingsReadsTheProfilesAndChoosingOneReadsItsSites(t *testing.T) {
	panelsPorted(t, func() {
		_, read := panelsReduce(t, panelsWorkspace(), `{"type":"browser-import.read"}`)
		panelsEqual(t, read.Effects, `[{"type":"browser-import.list-sources"}]`, "settings reads the profiles")
		chosen, transition := panelsReduce(t, panelsSourcesListed(t), `{"type":"browser-import.choose","sourceId":"brave:0:Default"}`)
		panelsEqual(t, transition.Effects, `[{"type":"browser-import.list-sites","sourceId":"brave:0:Default"}]`, "choosing one reads its sites")
		if chosen.BrowserImport.Picker == nil || chosen.BrowserImport.Picker.Sites != nil {
			t.Error("the sites are being read")
		}
		if unknown, _ := panelsReduce(t, panelsSourcesListed(t), `{"type":"browser-import.choose","sourceId":"chrome:0:Default"}`); unknown.BrowserImport.Picker != nil {
			t.Error("only a profile that was found can be picked")
		}
	})
}

func TestTickedSitesAreImportedRememberedAndImportedAgain(t *testing.T) {
	panelsPorted(t, func() {
		picked := panelsRun(t, panelsSourcesListed(t),
			`{"type":"browser-import.choose","sourceId":"brave:0:Default"}`,
			`{"type":"browser-import.sites","sourceId":"brave:0:Default","sites":[{"domain":"example.com","cookies":2},{"domain":"github.com","cookies":5}]}`,
			`{"type":"browser-import.toggle","site":"github.com"}`,
			`{"type":"browser-import.toggle","site":"not-offered.com"}`,
		)
		if !slices.Equal(picked.BrowserImport.Picker.Selected, []string{"github.com"}) {
			t.Fatal("only an offered site is ticked")
		}
		started, transition := panelsReduce(t, picked, `{"type":"browser-import.run"}`)
		if !started.BrowserImport.Importing {
			t.Error("the import is going")
		}
		panelsEqual(t, panelsEffectOf(t, transition, "browser-import.import"), `{"type":"browser-import.import","sourceId":"brave:0:Default","sites":["github.com"]}`, "the ticked sites are imported")
		if _, twice := panelsReduce(t, started, `{"type":"browser-import.run"}`); len(twice.Effects) != 0 {
			t.Error("one import at a time")
		}
		done, transition := panelsReduce(t, started, `{"type":"browser-import.done","sourceId":"brave:0:Default","sites":["github.com"],"result":{"imported":5,"skipped":1}}`)
		if done.BrowserImport.Picker != nil || deref(done.BrowserImport.Notice) != "Copied 5 cookies for github.com from Brave. 1 could not be read." {
			t.Errorf("the import says how it went: %q", deref(done.BrowserImport.Notice))
		}
		panelsEqual(t, done.BrowserImport.Last, `{"sourceId":"brave:0:Default","sites":["github.com"]}`, "the import is remembered")
		panelsEqual(t, panelsEffectOf(t, transition, "persist-preferences")["preferences"].(map[string]any)["browserImport"], `{"sourceId":"brave:0:Default","sites":["github.com"]}`, "and saved")
		_, again := panelsReduce(t, done, `{"type":"browser-import.again"}`)
		panelsEqual(t, panelsEffectOf(t, again, "browser-import.import"), `{"type":"browser-import.import","sourceId":"brave:0:Default","sites":["github.com"]}`, "it imports again on request")
		reopened := panelsRun(t, done,
			`{"type":"browser-import.choose","sourceId":"brave:0:Default"}`,
			`{"type":"browser-import.sites","sourceId":"brave:0:Default","sites":[{"domain":"github.com","cookies":5}]}`,
		)
		if !slices.Equal(reopened.BrowserImport.Picker.Selected, []string{"github.com"}) {
			t.Error("the last import's sites start ticked")
		}
	})
}

func TestAFailedImportIsShownBesideItAndNotRemembered(t *testing.T) {
	panelsPorted(t, func() {
		reading := panelsRun(t, panelsSourcesListed(t), `{"type":"browser-import.choose","sourceId":"firefox:0:Profiles/a"}`, `{"type":"browser-import.failed","message":"That browser profile is no longer on this computer."}`)
		if reading.BrowserImport.Picker != nil || deref(reading.BrowserImport.Error) != "That browser profile is no longer on this computer." || reading.ActionError != nil {
			t.Error("a failure is shown beside the import")
		}
		empty := panelsRun(t, panelsSourcesListed(t),
			`{"type":"browser-import.choose","sourceId":"brave:0:Default"}`,
			`{"type":"browser-import.sites","sourceId":"brave:0:Default","sites":[{"domain":"github.com","cookies":5}]}`,
			`{"type":"browser-import.toggle","site":"github.com"}`,
			`{"type":"browser-import.run"}`,
			`{"type":"browser-import.done","sourceId":"brave:0:Default","sites":["github.com"],"result":{"imported":0,"skipped":5}}`,
		)
		if deref(empty.BrowserImport.Error) != "No cookies could be copied from Brave." || empty.BrowserImport.Last != nil || empty.BrowserImport.Picker == nil {
			t.Error("an import that copied nothing is not remembered, and the picks stay")
		}
	})
}
