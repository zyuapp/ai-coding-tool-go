package opencode

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/opencode/client"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// What the app asks of a server. *client.Server fits; a scripted one can stand in for it. It must be
// comparable (a pointer), since lists already loaded are remembered per server.
type Client interface {
	client.Requester
	// Calls listener for every event until the server goes; the returned function stops listening.
	Subscribe(listener func(client.Event)) func()
	// Closed once the server has gone.
	Exited() <-chan struct{}
	// How the server ended, once it has.
	Exit() *client.ExitStatus
	Close()
}

type ServerOptions struct {
	// Folder the server starts in, which is the location a request names none for.
	Cwd string
	// Inline configuration, merged over the user's own. Nil sends none.
	Config any
	// Told the server's process id once it starts. May be nil.
	OnSpawn func(pid int)
}

type Connect func(ctx context.Context, options ServerOptions) (Client, error)

// Starts a private OpenCode server on the loopback interface, on a port of the system's choosing,
// behind the password it makes up for itself, and connects to it.
func StartServer(ctx context.Context, options ServerOptions) (Client, error) {
	executable, ok := agent.EngineBinaryPath(Binary)
	if !ok {
		executable = Binary.Command
	}
	server, err := client.Start(ctx, client.Options{
		Cwd:        options.Cwd,
		Config:     options.Config,
		Executable: executable,
		Make: func(name string, args ...string) *exec.Cmd {
			return platform.Command(context.Background(), name, args...)
		},
		OnSpawn: options.OnSpawn,
	})
	if err != nil {
		return nil, err
	}
	return server, nil
}

// Sends a request and decodes its answer; an empty or null answer is T's zero value.
func requestAs[T any](ctx context.Context, c client.Requester, method, path string, body any, timeout time.Duration) (T, error) {
	var value T
	raw, err := c.Request(ctx, method, path, body, timeout)
	if err != nil || len(raw) == 0 || raw.Kind() == 'n' {
		return value, err
	}
	return value, jsonx.Unmarshal(raw, &value)
}

// The event data and answers the server sends, read as loosely as the TypeScript reads them.
type record map[string]jsontext.Value

func recordOf(raw jsontext.Value) (record, bool) {
	if len(raw) == 0 || raw.Kind() != '{' {
		return nil, false
	}
	var fields record
	if jsonx.Unmarshal(raw, &fields) != nil {
		return nil, false
	}
	return fields, true
}

// The member's string value; false when it is absent or not a string.
func (r record) text(key string) (string, bool) {
	raw, ok := r[key]
	if !ok || raw.Kind() != '"' {
		return "", false
	}
	var value string
	if jsonx.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func (r record) object(key string) (record, bool) {
	return recordOf(r[key])
}

// JavaScript's String(value) of a member, "undefined" when it is absent.
func (r record) jsString(key string) string {
	raw, ok := r[key]
	if !ok {
		return "undefined"
	}
	switch raw.Kind() {
	case '"':
		value, _ := r.text(key)
		return value
	case '0':
		var number float64
		if jsonx.Unmarshal(raw, &number) != nil {
			return string(raw)
		}
		return jsNumber(number)
	case '{':
		return "[object Object]"
	case '[':
		var items []jsontext.Value
		_ = jsonx.Unmarshal(raw, &items)
		parts := make([]string, len(items))
		for index, item := range items {
			if item.Kind() != 'n' {
				parts[index] = record{"v": item}.jsString("v")
			}
		}
		return strings.Join(parts, ",")
	default:
		return string(raw)
	}
}

func jsNumber(number float64) string {
	if number == 0 {
		return "0"
	}
	if math.Abs(number) < 1e21 && math.Abs(number) >= 1e-6 {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	text := strconv.FormatFloat(number, 'e', -1, 64)
	mantissa, exponent, _ := strings.Cut(text, "e")
	exponent = strings.TrimLeft(strings.TrimPrefix(exponent, "+"), "0")
	if strings.HasPrefix(exponent, "-") {
		return mantissa + "e-" + strings.TrimLeft(exponent[1:], "0")
	}
	return mantissa + "e+" + exponent
}

// JavaScript's String.prototype.trim.
func jsTrim(text string) string {
	return strings.TrimFunc(text, isJSSpace)
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// JavaScript's encodeURIComponent.
func encodeURIComponent(text string) string {
	var out strings.Builder
	for index := 0; index < len(text); index++ {
		c := text[index]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			out.WriteByte(c)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", c)
	}
	return out.String()
}

func locationQuery(directory string) string {
	return "location[directory]=" + encodeURIComponent(directory)
}
