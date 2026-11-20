package speech

import (
	"encoding/binary"
	"math"
	"slices"
	"strings"
	"testing"
)

type wavOptions struct {
	tag, channels, rate, bits int
	data                      []byte
	subformat                 int
	extensible                bool
}

func wav(options wavOptions) []byte {
	size := 16
	if options.extensible {
		size = 40
	}
	if options.tag == 0 {
		options.tag = 1
	}
	format := make([]byte, size)
	binary.LittleEndian.PutUint16(format[0:], uint16(options.tag))
	binary.LittleEndian.PutUint16(format[2:], uint16(options.channels))
	binary.LittleEndian.PutUint32(format[4:], uint32(options.rate))
	binary.LittleEndian.PutUint32(format[8:], uint32(options.rate*options.channels*(options.bits/8)))
	binary.LittleEndian.PutUint16(format[12:], uint16(options.channels*(options.bits/8)))
	binary.LittleEndian.PutUint16(format[14:], uint16(options.bits))
	if options.extensible {
		binary.LittleEndian.PutUint16(format[16:], 22)
		binary.LittleEndian.PutUint16(format[24:], uint16(options.subformat))
	}
	chunk := func(id string, body []byte) []byte {
		head := make([]byte, 8)
		copy(head, id)
		binary.LittleEndian.PutUint32(head[4:], uint32(len(body)))
		out := append(head, body...)
		if len(body)%2 == 1 {
			out = append(out, 0)
		}
		return out
	}
	body := slices.Concat([]byte("WAVE"), chunk("fmt ", format), chunk("LIST", []byte("odd")), chunk("data", options.data))
	riff := make([]byte, 8)
	copy(riff, "RIFF")
	binary.LittleEndian.PutUint32(riff[4:], uint32(len(body)))
	return append(riff, body...)
}

func int16s(values ...int16) []byte {
	out := make([]byte, len(values)*2)
	for index, value := range values {
		binary.LittleEndian.PutUint16(out[index*2:], uint16(value))
	}
	return out
}

func TestPCM16SamplesReadAsFractionsDroppingAnOddByte(t *testing.T) {
	got := PCM16Samples(append(int16s(0, 16_384, -32_768), 7))
	if !slices.Equal(got, []float32{0, 0.5, -1}) {
		t.Fatalf("got %v", got)
	}
}

func TestAudioLevel(t *testing.T) {
	if AudioLevel(make([]float32, 160)) != 0 || AudioLevel(nil) != 0 {
		t.Fatal("silence and nothing read as no level")
	}
	loud := make([]float32, 160)
	quiet := make([]float32, 160)
	for index := range loud {
		loud[index], quiet[index] = -1, -0.01
		if index%2 == 1 {
			loud[index], quiet[index] = 1, 0.01
		}
	}
	if level := AudioLevel(loud); level != 1 {
		t.Fatalf("full scale read %v", level)
	}
	if level := AudioLevel(quiet); !(level > 0 && level < 0.5) {
		t.Fatalf("a quiet voice reads low but above silence, read %v", level)
	}
}

func TestStereoWAVIsMixedToMonoPastUnknownChunks(t *testing.T) {
	samples, rate, err := WAVSamples(wav(wavOptions{channels: 2, rate: 44_100, bits: 16, data: int16s(16_384, 0, -32_768, 0)}))
	if err != nil || rate != 44_100 || !slices.Equal(samples, []float32{0.25, -0.5}) {
		t.Fatalf("got %v %v %v", samples, rate, err)
	}
}

func TestFloatWAVsRead(t *testing.T) {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:], math.Float32bits(0.5))
	binary.LittleEndian.PutUint32(data[4:], math.Float32bits(-0.25))
	for _, options := range []wavOptions{
		{tag: 3, channels: 1, rate: 16_000, bits: 32, data: data},
		{tag: 0xfffe, extensible: true, subformat: 3, channels: 1, rate: 16_000, bits: 32, data: data},
	} {
		samples, _, err := WAVSamples(wav(options))
		if err != nil || !slices.Equal(samples, []float32{0.5, -0.25}) {
			t.Fatalf("got %v %v", samples, err)
		}
	}
}

func TestUnreadableWAVsAreRefused(t *testing.T) {
	expect := func(data []byte, text string) {
		t.Helper()
		if _, _, err := WAVSamples(data); err == nil || !strings.Contains(err.Error(), text) {
			t.Fatalf("want %q, got %v", text, err)
		}
	}
	expect([]byte("not a wav at all"), "Not a WAV")
	expect(wav(wavOptions{channels: 1, rate: 16_000, bits: 8, data: make([]byte, 4)}), "format")
	expect(wav(wavOptions{channels: 1, rate: 16_000, bits: 16})[:12+8+16], "no sound")
}

func TestResampled(t *testing.T) {
	same := []float32{0.1, 0.2}
	if got := Resampled(same, 16_000, 16_000); &got[0] != &same[0] {
		t.Fatal("sound at its own rate is left alone")
	}
	down := Resampled([]float32{0, 0.75, 0.25, 0.25, 0.5, 0.5}, 48_000, 16_000)
	rounded := make([]float64, len(down))
	for index, value := range down {
		rounded[index] = math.Round(float64(value)*1000) / 1000
	}
	if !slices.Equal(rounded, []float64{0.333, 0.417}) {
		t.Fatalf("down: %v", rounded)
	}
	if up := Resampled([]float32{0, 1}, 8_000, 16_000); !slices.Equal(up, []float32{0, 0.5, 1, 1}) {
		t.Fatalf("up: %v", up)
	}
	if length := len(Resampled(make([]float32, 44_100), 44_100, 16_000)); length != 16_000 {
		t.Fatalf("length %d", length)
	}
}

func TestVocabularyAndText(t *testing.T) {
	vocabulary := ParakeetVocabularyPieces("<unk> 0\n▁hel 1\nlo 2\n▁world 3\n, 4\n▁ 5\n<blk> 6\n")
	if len(vocabulary) != 7 || slices.Index(vocabulary, "<blk>") != 6 {
		t.Fatalf("vocabulary %q", vocabulary)
	}
	pick := func(ids ...int) []string {
		var pieces []string
		for _, id := range ids {
			pieces = append(pieces, vocabulary[id])
		}
		return pieces
	}
	if text := ParakeetText(pick(1, 2, 4, 3)); text != "hello, world" {
		t.Fatalf("text %q", text)
	}
	if text := ParakeetText(pick(5, 5)); text != "" {
		t.Fatalf("spaces %q", text)
	}
}

func TestModelPlaces(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if place := DictationModelPlace("linux", "amd64"); place == nil || place.Folder != "/data/aicodingtool/models/parakeet-tdt-0.6b-v2-int8" {
		t.Fatalf("linux place %v", place)
	}
	if place := DictationModelPlace("linux", "arm64"); place == nil || !slices.Equal(place.Files, []string{"nemo128.onnx", "encoder-model.int8.onnx", "decoder_joint-model.int8.onnx", "vocab.txt"}) {
		t.Fatalf("linux files %v", place)
	}
	t.Setenv("XDG_DATA_HOME", "relative")
	if place := DictationModelPlace("linux", "amd64"); place == nil || !strings.HasSuffix(place.Folder, "/.local/share/aicodingtool/models/parakeet-tdt-0.6b-v2-int8") {
		t.Fatalf("relative XDG_DATA_HOME %v", place)
	}
	if place := DictationModelPlace("darwin", "arm64"); place == nil || !strings.HasSuffix(place.Folder, "FluidAudio/Models/parakeet-tdt-0.6b-v2-coreml") {
		t.Fatalf("mac place %v", place)
	}
	if DictationModelPlace("darwin", "amd64") != nil || DictationModelPlace("windows", "amd64") != nil {
		t.Fatal("no place elsewhere")
	}
}
