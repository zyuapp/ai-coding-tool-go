package contracts

import "testing"

func TestMobileClientMessagesAreReadDefensively(t *testing.T) {
	for json, want := range map[string]bool{
		`{"kind":"pair","version":8,"code":"ABCD2345","deviceName":"iPhone"}`:                      true,
		`{"kind":"pair","version":8,"code":"ABCD2345"}`:                                            false,
		`{"kind":"pair","version":"1","code":"ABCD2345","deviceName":"iPhone"}`:                    false,
		`{"kind":"pair","version":8,"oldestComputer":8,"code":"ABCD2345","deviceName":"iPhone"}`:   true,
		`{"kind":"pair","version":8,"oldestComputer":"8","code":"ABCD2345","deviceName":"iPhone"}`: false,
		`{"kind":"resume","version":1,"oldestComputer":1,"token":"abc","lastSequence":0}`:          true,
		`{"kind":"resume","version":1,"oldestComputer":-1,"token":"abc","lastSequence":0}`:         false,
		`{"kind":"resume","version":1,"token":"abc","lastSequence":0}`:                             true,
		`{"kind":"resume","version":1,"token":"abc","sessionId":"session-1","lastSequence":12}`:    true,
		`{"kind":"resume","version":1,"token":"abc","lastSequence":-1}`:                            false,
		`{"kind":"resume","version":1,"token":"abc","lastSequence":1.5}`:                           false,
		`{"kind":"command","requestId":"request-1","command":{"type":"run.cancel"}}`:               true,
		`{"kind":"command","requestId":"request-1","command":{"type":"terminal.open"}}`:            false,
		`{"kind":"command","command":{"type":"run.cancel"}}`:                                       false,
		`{"kind":"pong","at":1800000000000}`:                                                       true,
		`{"kind":"ping","at":1800000000000}`:                                                       true,
		`{"kind":"push-token","push":null}`:                                                        true,
		`{"kind":"push-token"}`:                                                                    false,
		`{"kind":"hello"}`:                                                                         false,
	} {
		restExpect(t, "IsMobileClientMessage", IsMobileClientMessage, restObj(t, json), want)
	}
	if MobileProtocolVersion < OldestPhoneProtocolVersion {
		t.Fatal("the computer serves an older protocol than it accepts")
	}
}

func TestAPhonesRequestTravelsToTheWindowAndIsAnsweredThere(t *testing.T) {
	restExpect(t, "IsMobileRequest", IsMobileRequest, restObj(t, `{"type":"mobile.request","requestId":"request-1","sessionId":"session-1","op":"snapshot"}`), true)
	restExpect(t, "IsMobileRequest", IsMobileRequest, restObj(t, `{"type":"mobile.request","requestId":"request-1","sessionId":"session-1","op":"snapshot","answerBy":1800000000000}`), true)
	restExpect(t, "IsMobileRequest", IsMobileRequest, restObj(t, `{"type":"mobile.request","requestId":"request-1","sessionId":"session-1","op":"snapshot","answerBy":null}`), false)
	restExpect(t, "IsMobileRequest", IsMobileRequest, restObj(t, `{"type":"mobile.request","requestId":"request-1","sessionId":"session-1","op":"command","command":{"type":"task.dismiss-all"}}`), true)
	restExpect(t, "IsMobileRequest", IsMobileRequest, restObj(t, `{"type":"mobile.request","requestId":"request-1","sessionId":"session-1","op":"command","command":{"type":"browser.new-tab"}}`), false)
	restExpect(t, "IsMobileRequest", IsMobileRequest, restObj(t, `{"type":"thread.request","requestId":"request-1","sessionId":"session-1","op":"snapshot"}`), false)
	restExpect(t, "IsMobileResponse", IsMobileResponse, restObj(t, `{"type":"mobile.response","requestId":"request-1","ok":true,"result":null}`), true)
	restExpect(t, "IsMobileResponse", IsMobileResponse, restObj(t, `{"type":"mobile.response","requestId":"request-1","ok":false,"message":"refused"}`), true)
	restExpect(t, "IsMobileResponse", IsMobileResponse, restObj(t, `{"type":"mobile.response","requestId":"request-1","ok":false}`), false)
}

func TestAMobileQueryNamesAThreadAndAComparison(t *testing.T) {
	summary := `{"kind":"diff-summary","taskId":"task-1","range":{"kind":"uncommitted"}}`
	patch := `{"kind":"diff-patch","taskId":"task-1","range":{"kind":"branches","base":"main","compare":null},"path":"src/a.ts","previousPath":"src/b.ts"}`
	query := func(json string) any { return restJSON(t, `{"kind":"query","requestId":"q1","query":`+json+`}`) }
	restExpect(t, "IsMobileClientMessage", IsMobileClientMessage, query(summary), true)
	restExpect(t, "IsMobileClientMessage", IsMobileClientMessage, query(patch), true)
	restExpect(t, "IsMobileRequest", IsMobileRequest, restJSON(t, `{"type":"mobile.request","requestId":"r1","sessionId":"s1","op":"query","query":`+summary+`}`), true)
	for json, want := range map[string]bool{
		`{"kind":"diff-patch","taskId":"task-1","range":{"kind":"uncommitted"}}`:              false,
		`{"kind":"diff-summary","taskId":"task-1","range":{"kind":"commit","commit":"nope"}}`: false,
		`{"kind":"read-file","taskId":"task-1","path":"/etc/passwd"}`:                         false,
		`{"kind":"directories","prefix":"~/code/"}`:                                           true,
		`{"kind":"directories","prefix":""}`:                                                  true,
		`{"kind":"directories","prefix":"/a\u0000b"}`:                                         false,
		`{"kind":"directories","prefix":"` + restRepeat("x", 4_097) + `"}`:                    false,
		`{"kind":"directories"}`:                                                              false,
		`{"kind":"thread","taskId":"task-2"}`:                                                 true,
		`{"kind":"thread"}`:                                                                   false,
		`{"kind":"history","taskId":"task-2","before":40}`:                                    true,
		`{"kind":"history","taskId":"task-2","before":-1}`:                                    false,
		`{"kind":"history","taskId":"task-2","before":1.5}`:                                   false,
		`{"kind":"archived"}`:                                                                 true,
		`{"kind":"commands"}`:                                                                 true,
		`{"kind":"commands","taskId":"task-1"}`:                                               true,
		`{"kind":"commands","taskId":""}`:                                                     false,
		`{"kind":"image","taskId":"t","messageId":"m","href":"shot.png"}`:                     true,
		`{"kind":"image","taskId":"t","messageId":"m","href":"shot.png","preview":true}`:      true,
		`{"kind":"image","taskId":"t","messageId":"m","href":"shot.png","preview":"yes"}`:     false,
		`{"kind":"transcribe","audio":"UklG"}`:                                                true,
		`{"kind":"transcribe","audio":"UklGR"}`:                                               false,
	} {
		restExpect(t, "IsMobileClientMessage", IsMobileClientMessage, query(json), want)
	}
}
