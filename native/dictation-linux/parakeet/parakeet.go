//go:build cgo

// Package parakeet runs Parakeet TDT through ONNX Runtime, from the ONNX export onnx-asr reads: a
// preprocessor turns sound into mel features, the encoder turns those into frames, and a joint
// decoder walks the frames, choosing a token and how many frames to skip at each step.
package parakeet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/zyuapp/ai-coding-tool-go/internal/devices/speech"
)

// maxTokensPerFrame is how many tokens one frame may emit before the walk moves on regardless.
const maxTokensPerFrame = 10

var environment struct {
	sync.Mutex
	err  error
	done bool
}

// Init loads ONNX Runtime's shared library from library, once per process.
func Init(library string) error {
	environment.Lock()
	defer environment.Unlock()
	if !environment.done {
		environment.done = true
		ort.SetSharedLibraryPath(library)
		environment.err = ort.InitializeEnvironment()
	}
	return environment.err
}

// Parakeet holds the three graphs, loaded once; each transcription reuses them.
type Parakeet struct {
	preprocessor, encoder, decoder *ort.DynamicAdvancedSession
	vocabulary                     []string
	blank                          int
	layers, hidden                 int64
}

// declared is the size of a fixed axis an input declares, or fallback when it does not say.
func declared(file string, input string, axis int, fallback int64) int64 {
	inputs, _, err := ort.GetInputOutputInfo(file)
	if err != nil {
		return fallback
	}
	for _, entry := range inputs {
		if entry.Name == input && entry.OrtValueType == ort.ONNXTypeTensor && axis < len(entry.Dimensions) && entry.Dimensions[axis] > 0 {
			return entry.Dimensions[axis]
		}
	}
	return fallback
}

// Load reads the model in folder. Init must have succeeded first.
func Load(folder string, threads int) (*Parakeet, error) {
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer options.Destroy()
	if err := options.SetIntraOpNumThreads(threads); err != nil {
		return nil, err
	}
	if err := options.SetInterOpNumThreads(1); err != nil {
		return nil, err
	}
	if err := options.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); err != nil {
		return nil, err
	}
	file := func(name string) string { return filepath.Join(folder, name) }
	model := &Parakeet{}
	type opened struct {
		session *ort.DynamicAdvancedSession
		err     error
	}
	open := func(name string, inputs, outputs []string) <-chan opened {
		answer := make(chan opened, 1)
		go func() {
			session, err := ort.NewDynamicAdvancedSession(file(name), inputs, outputs, options)
			answer <- opened{session, err}
		}()
		return answer
	}
	preprocessor := open(speech.ParakeetPreprocessor, []string{"waveforms", "waveforms_lens"}, []string{"features", "features_lens"})
	encoder := open(speech.ParakeetEncoder, []string{"audio_signal", "length"}, []string{"outputs", "encoded_lengths"})
	decoder := open(speech.ParakeetDecoder, []string{"encoder_outputs", "targets", "target_length", "input_states_1", "input_states_2"}, []string{"outputs", "output_states_1", "output_states_2"})
	var failure error
	for _, pending := range []struct {
		into   **ort.DynamicAdvancedSession
		answer <-chan opened
	}{{&model.preprocessor, preprocessor}, {&model.encoder, encoder}, {&model.decoder, decoder}} {
		result := <-pending.answer
		*pending.into = result.session
		failure = errors.Join(failure, result.err)
	}
	text, err := os.ReadFile(file(speech.ParakeetVocabulary))
	failure = errors.Join(failure, err)
	if failure != nil {
		model.Close()
		return nil, failure
	}
	model.vocabulary = speech.ParakeetVocabularyPieces(string(text))
	model.blank = slices.Index(model.vocabulary, speech.ParakeetBlank)
	if model.blank < 0 {
		model.Close()
		return nil, errors.New("The speech model's vocabulary has no blank.")
	}
	model.layers = declared(file(speech.ParakeetDecoder), "input_states_1", 0, 2)
	model.hidden = declared(file(speech.ParakeetDecoder), "input_states_1", 2, 640)
	return model, nil
}

// Close releases the graphs.
func (p *Parakeet) Close() {
	for _, session := range []*ort.DynamicAdvancedSession{p.preprocessor, p.encoder, p.decoder} {
		if session != nil {
			_ = session.Destroy()
		}
	}
}

func argmax(values []float32, from, to int) int {
	best := from
	for index := from + 1; index < to; index++ {
		if values[index] > values[best] {
			best = index
		}
	}
	return best
}

func destroy(values ...ort.Value) {
	for _, value := range values {
		if value != nil {
			_ = value.Destroy()
		}
	}
}

// Transcribe turns 16 kHz mono sound into words.
func (p *Parakeet) Transcribe(samples []float32) (string, error) {
	waveforms, err := ort.NewTensor(ort.NewShape(1, int64(len(samples))), samples)
	if err != nil {
		return "", err
	}
	defer destroy(waveforms)
	lengths, err := ort.NewTensor(ort.NewShape(1), []int64{int64(len(samples))})
	if err != nil {
		return "", err
	}
	defer destroy(lengths)
	features := []ort.Value{nil, nil}
	if err := p.preprocessor.Run([]ort.Value{waveforms, lengths}, features); err != nil {
		return "", err
	}
	defer destroy(features...)
	encoded := []ort.Value{nil, nil}
	if err := p.encoder.Run(features, encoded); err != nil {
		return "", err
	}
	defer destroy(encoded...)
	frames, ok := encoded[0].(*ort.Tensor[float32])
	if !ok {
		return "", fmt.Errorf("unexpected encoder output %T", encoded[0])
	}
	encodedLengths, ok := encoded[1].(*ort.Tensor[int64])
	if !ok || len(encodedLengths.GetData()) == 0 {
		return "", fmt.Errorf("unexpected encoder lengths %T", encoded[1])
	}
	dims := frames.GetShape()
	var width, stride int
	if len(dims) > 1 {
		width = int(dims[1])
	}
	if len(dims) > 2 {
		stride = int(dims[2])
	}
	length := int(encodedLengths.GetData()[0])
	data := frames.GetData()

	frame, err := ort.NewEmptyTensor[float32](ort.NewShape(1, int64(width), 1))
	if err != nil {
		return "", err
	}
	defer destroy(frame)
	targets, err := ort.NewEmptyTensor[int32](ort.NewShape(1, 1))
	if err != nil {
		return "", err
	}
	defer destroy(targets)
	targetLength, err := ort.NewTensor(ort.NewShape(1), []int32{1})
	if err != nil {
		return "", err
	}
	defer destroy(targetLength)
	emptyState := func() (ort.Value, error) {
		return ort.NewEmptyTensor[float32](ort.NewShape(p.layers, 1, p.hidden))
	}
	first, err := emptyState()
	if err != nil {
		return "", err
	}
	second, err := emptyState()
	if err != nil {
		destroy(first)
		return "", err
	}
	states := [2]ort.Value{first, second}
	defer func() { destroy(states[0], states[1]) }()

	tokens := len(p.vocabulary)
	var said []int
	frameTokens := 0
	for at := 0; at < length; {
		frameData := frame.GetData()
		for index := range width {
			frameData[index] = data[index*stride+at]
		}
		last := p.blank
		if len(said) > 0 {
			last = said[len(said)-1]
		}
		targets.GetData()[0] = int32(last)
		step := []ort.Value{nil, nil, nil}
		if err := p.decoder.Run([]ort.Value{frame, targets, targetLength, states[0], states[1]}, step); err != nil {
			return "", err
		}
		logitsTensor, ok := step[0].(*ort.Tensor[float32])
		if !ok {
			destroy(step...)
			return "", fmt.Errorf("unexpected decoder output %T", step[0])
		}
		logits := logitsTensor.GetData()
		token := argmax(logits, 0, tokens)
		skip := argmax(logits, tokens, len(logits)) - tokens
		destroy(step[0])
		if token != p.blank {
			destroy(states[0], states[1])
			states = [2]ort.Value{step[1], step[2]}
			said = append(said, token)
			frameTokens++
		} else {
			destroy(step[1], step[2])
		}
		if skip > 0 {
			at += skip
			frameTokens = 0
		} else if token == p.blank || frameTokens == maxTokensPerFrame {
			at++
			frameTokens = 0
		}
	}
	pieces := make([]string, len(said))
	for index, id := range said {
		if id < len(p.vocabulary) {
			pieces[index] = p.vocabulary[id]
		}
	}
	return speech.ParakeetText(pieces), nil
}
