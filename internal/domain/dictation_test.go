package domain

import "testing"

func TestSpokenWordsJoinTyping(t *testing.T) {
	cases := [][3]string{{"", " hello ", "hello"}, {"Fix it.", "Now.", "Fix it. Now."}, {"Fix it.\n", "Now.", "Fix it.\nNow."}, {"Fix it.", "", "Fix it."}}
	for _, c := range cases {
		if got := WithSpokenWords(c[0], c[1]); got != c[2] {
			t.Errorf("%q + %q = %q", c[0], c[1], got)
		}
	}
}
