package domain

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestVisualFrameMessageTakesOnlyWellFormedMessages(t *testing.T) {
	size := func(height any) any { return map[string]any{"type": "aicodingtool-visual:size", "height": height} }
	cases := []struct {
		data any
		want VisualFrameMessage
	}{
		{size(240.2), VisualFrameMessageSize{Height: 241}},
		{size(1e9), VisualFrameMessageSize{Height: MaxVisualHeight}},
		{size(-4.0), VisualFrameMessageSize{Height: 0}},
		{size(math.NaN()), nil},
		{size("240"), nil},
		{map[string]any{"type": "something-else", "height": 240.0}, nil},
		{"aicodingtool-visual:size", nil},
		{nil, nil},
		{map[string]any{"type": "aicodingtool-visual:error", "message": strings.Repeat("x", 900)}, VisualFrameMessageError{Message: strings.Repeat("x", 500)}},
	}
	for _, c := range cases {
		if got := VisualFrameVisualFrameMessage(c.data); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %#v", c.data, got)
		}
	}
}
