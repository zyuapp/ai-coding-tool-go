package devices

import (
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// SimulatorHelperMessage is what the simulator helper says: a JSON answer or event, or a piece of a
// screen stream. Exactly one of JSON and Frame is set.
type SimulatorHelperMessage struct {
	JSON  map[string]any
	Frame domain.SimulatorFrame
}

const (
	simulatorJSONKind   = 0
	simulatorChunkKind  = 1
	simulatorConfigKind = 2
	// maxFrameBytes is far beyond any frame a phone's screen encodes to, so a corrupt length is caught rather than awaited.
	maxFrameBytes = 64 * 1024 * 1024
)

var errMalformedFrame = errors.New("The simulator helper sent a malformed frame.")

func simulatorBinary(kind byte, payload []byte) domain.SimulatorFrame {
	if len(payload) < 2 {
		return nil
	}
	headerLength := int(binary.BigEndian.Uint16(payload))
	if len(payload) < 2+headerLength {
		return nil
	}
	var header map[string]any
	if err := jsonx.Unmarshal(payload[2:2+headerLength], &header); err != nil || header == nil {
		return nil
	}
	udid, ok := header["udid"].(string)
	if !ok {
		return nil
	}
	data := append([]byte{}, payload[2+headerLength:]...)
	if kind == simulatorConfigKind {
		return domain.SimulatorFrameConfig{UDID: udid, Width: jsNumber(header["width"]), Height: jsNumber(header["height"]), Data: data}
	}
	return domain.SimulatorFrameChunk{UDID: udid, Key: header["key"] == true, Timestamp: jsNumber(header["ts"]), Data: data}
}

// jsNumber is Number(value) for a value read from JSON, with 0 for what is not a number so it still encodes.
func jsNumber(value any) float64 {
	switch value := value.(type) {
	case float64:
		return value
	case bool:
		if value {
			return 1
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && !math.IsInf(parsed, 0) {
			return parsed
		}
	}
	return 0
}

// SimulatorFrameReader splits the helper's stdout, `[u32 length][u8 kind][payload]` after another,
// however the pipe chunks it.
type SimulatorFrameReader struct {
	deliver  func(SimulatorHelperMessage)
	buffered []byte
}

func NewSimulatorFrameReader(deliver func(SimulatorHelperMessage)) *SimulatorFrameReader {
	return &SimulatorFrameReader{deliver: deliver}
}

// Push takes the next piece of the stream. A frame that does not parse is dropped, since the next
// keyframe recovers the stream; a length that cannot be right is an error.
func (r *SimulatorFrameReader) Push(chunk []byte) error {
	r.buffered = append(r.buffered, chunk...)
	for len(r.buffered) >= 4 {
		length := binary.BigEndian.Uint32(r.buffered)
		if length < 1 || length > maxFrameBytes {
			return errMalformedFrame
		}
		if len(r.buffered) < 4+int(length) {
			return nil
		}
		body := r.buffered[4 : 4+length]
		r.buffered = r.buffered[4+length:]
		kind, payload := body[0], body[1:]
		switch kind {
		case simulatorJSONKind:
			var value any
			if jsonx.Unmarshal(payload, &value) == nil {
				switch object := value.(type) {
				case map[string]any:
					r.deliver(SimulatorHelperMessage{JSON: object})
				case []any:
					r.deliver(SimulatorHelperMessage{JSON: map[string]any{}})
				}
			}
		case simulatorChunkKind, simulatorConfigKind:
			if frame := simulatorBinary(kind, payload); frame != nil {
				r.deliver(SimulatorHelperMessage{Frame: frame})
			}
		}
	}
	if len(r.buffered) == 0 {
		r.buffered = nil
	}
	return nil
}
