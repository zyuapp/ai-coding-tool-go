package codex

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

// Pending server requests are withdrawn when Codex resolves them or their owning turn ends.
type CodexQuestions struct {
	mu sync.Mutex
	// By the request's id as written.
	pending map[string]context.CancelFunc
}

func NewCodexQuestions() *CodexQuestions {
	return &CodexQuestions{pending: map[string]context.CancelFunc{}}
}

// Withdraws the question the server asked with this request id, if it is still open.
func (questions *CodexQuestions) Withdraw(id protocol.RequestID) {
	questions.mu.Lock()
	cancel := questions.pending[string(id.Raw())]
	questions.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (questions *CodexQuestions) Answer(request *ServerRequest, params protocol.ToolRequestUserInputParams, input *agent.ProviderRunInput) {
	ctx, cancel := context.WithCancel(context.Background())
	key := string(request.ID.Raw())
	questions.mu.Lock()
	questions.pending[key] = cancel
	questions.mu.Unlock()
	asked := domain.QuestionRequest{Blocking: params.IsBlocking, Questions: make([]*domain.AgentQuestion, 0, len(params.Questions))}
	for _, question := range params.Questions {
		options := make([]*domain.QuestionOption, 0, len(question.Options))
		for _, option := range question.Options {
			options = append(options, &domain.QuestionOption{Label: option.Label, Description: option.Description})
		}
		asked.Questions = append(asked.Questions, &domain.AgentQuestion{ID: question.ID, Header: question.Header, Question: question.Question, Options: options})
	}
	go func() {
		defer func() {
			questions.mu.Lock()
			delete(questions.pending, key)
			questions.mu.Unlock()
		}()
		answers, ok := input.AskQuestion(ctx, asked)
		if ctx.Err() != nil {
			return
		}
		if !ok {
			request.Fail(protocol.JSONRPCError{Code: -32000, Message: "The question was closed without an answer."})
			return
		}
		response := protocol.ToolRequestUserInputResponse{Answers: map[string]protocol.ToolRequestUserInputAnswer{}}
		for id, answer := range answers {
			response.Answers[id] = protocol.ToolRequestUserInputAnswer{Answers: []string{answer}}
		}
		request.Respond(response)
	}()
}

// Puts an agent message's questions to the user, and steers the answers into the turn still going.
func (questions *CodexQuestions) AnswerAsync(native []protocol.AsyncUserInputQuestion, input *agent.ProviderRunInput, client CodexClient, threadID string, activeTurnID func() (string, bool)) error {
	asked := domain.QuestionRequest{Questions: make([]*domain.AgentQuestion, 0, len(native))}
	for index, question := range native {
		options := make([]*domain.QuestionOption, 0, len(question.Options))
		for _, label := range question.Options {
			options = append(options, &domain.QuestionOption{Label: label})
		}
		asked.Questions = append(asked.Questions, &domain.AgentQuestion{ID: strconv.Itoa(index), Header: "Question", Question: question.Title, Options: options})
	}
	answers, ok := input.AskQuestion(context.Background(), asked)
	turnID, active := activeTurnID()
	if !ok || answers == nil || !active {
		return nil
	}
	parts := make([]string, 0, len(asked.Questions))
	for _, question := range asked.Questions {
		answer, given := answers[question.ID]
		if !given {
			answer = "undefined"
		}
		parts = append(parts, question.Question+"\n"+answer)
	}
	_, err := client.Request(context.Background(), "turn/steer", protocol.TurnSteerParams{ThreadID: threadID, ExpectedTurnID: turnID, Input: []protocol.UserInput{protocol.Text(strings.Join(parts, "\n\n"))}})
	return err
}

func (questions *CodexQuestions) Close() {
	questions.mu.Lock()
	pending := questions.pending
	questions.pending = map[string]context.CancelFunc{}
	questions.mu.Unlock()
	for _, cancel := range pending {
		cancel()
	}
}
