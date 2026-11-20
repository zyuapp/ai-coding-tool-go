package domain

import "testing"

func TestComputerAddressDropsSchemesAndChecksPorts(t *testing.T) {
	cases := map[string]string{
		"https://mac.tail.ts.net:8080/": "mac.tail.ts.net:8080",
		" mac ":                         "mac",
		"[fd7a::1]:22":                  "[fd7a::1]:22",
		"host:70000":                    "",
		"host:0":                        "",
		"-bad.host":                     "",
	}
	for text, want := range cases {
		if got, _ := ComputerAddress(text); got != want {
			t.Errorf("%q: got %q", text, got)
		}
	}
	if got := OfflineMessage(&ComputerLink{Name: "mac", Status: ComputerStatusConnecting, Error: new("refused")}); got != "mac is reconnecting. refused" {
		t.Error(got)
	}
}
