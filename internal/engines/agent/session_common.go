package agent

import (
	"context"
	"strings"
	"time"
)

// How long an interrupted turn has to come back with a result before the session is given up on.
const InterruptGrace = 10 * time.Second

func FirstLine(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return line
}

// What went wrong, in one line.
func ReasonOf(err error) string {
	if err == nil {
		return ""
	}
	return FirstLine(err.Error())
}

// Why the session could not open, worded for the run that asked. Lost says the continuation itself is gone.
type OpenFailure struct {
	Message string
	Lost    bool
}

func (failure *OpenFailure) Error() string { return failure.Message }

// Serves the app's tools to a session that is opening, and nothing (nil) when the run is offered
// none. A session that ended while they were being served gives them back and opens no further.
func ServeTools(ctx context.Context, host ToolHost, sets []ServedToolSet, ended func() bool, endedMessage string) (*ServedTools, error) {
	var tools []BoundTool
	for _, set := range sets {
		tools = append(tools, set.Tools...)
	}
	if len(tools) == 0 {
		return nil, nil
	}
	served, err := host.Serve(ctx, tools)
	if err != nil {
		return nil, err
	}
	if ended() {
		if served.Release != nil {
			served.Release()
		}
		return nil, &OpenFailure{Message: endedMessage}
	}
	return &served, nil
}
