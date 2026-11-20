// Package speech is dictation's sound and model handling that needs no native code: samples from raw
// sound and WAV files, the speech model's place on disk, Parakeet's vocabulary, and the Linux
// dictation helper's protocol.
package speech

import (
	"encoding/binary"
	"errors"
	"math"
)

// PCM16Samples reads signed 16-bit little-endian samples as floats from -1 to 1. A trailing odd byte is dropped.
func PCM16Samples(bytes []byte) []float32 {
	samples := make([]float32, len(bytes)/2)
	for index := range samples {
		samples[index] = float32(float64(int16(binary.LittleEndian.Uint16(bytes[index*2:]))) / 32_768)
	}
	return samples
}

// AudioLevel is how loud a stretch of samples is, from 0 to 1 in two-decimal steps: -55 dB reads as
// silence and -5 dB as full.
func AudioLevel(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	energy := 0.0
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(energy / float64(len(samples)))
	level := math.Max(0, math.Min(1, (20*math.Log10(math.Max(rms, 1e-6))+55)/50))
	return jsRound(level*100) / 100
}

// Resampled is sound at another rate. Going down, each new sample averages the old ones it spans,
// which keeps most of what would fold back into the voice; going up, it reads between its two
// neighbours. Sound already at the rate is returned as is.
func Resampled(samples []float32, from, to float64) []float32 {
	if from == to || len(samples) == 0 {
		return samples
	}
	ratio := from / to
	output := make([]float32, max(1, int(jsRound(float64(len(samples))/ratio))))
	for index := range output {
		start := float64(index) * ratio
		if ratio > 1 {
			first := int(math.Floor(start))
			last := min(len(samples), max(first+1, int(math.Floor(start+ratio))))
			sum := 0.0
			for at := first; at < last; at++ {
				sum += float64(samples[at])
			}
			output[index] = float32(sum / float64(last-first))
		} else {
			before := min(len(samples)-1, int(math.Floor(start)))
			after := min(len(samples)-1, before+1)
			weight := start - float64(before)
			output[index] = float32(float64(samples[before])*(1-weight) + float64(samples[after])*weight)
		}
	}
	return output
}

const (
	wavPCM        = 1
	wavFloat      = 3
	wavExtensible = 0xfffe
)

// WAVSamples is a WAV file's sound, mixed to mono at its own rate. Reads 16-bit PCM and 32-bit
// float; anything else is refused.
func WAVSamples(bytes []byte) (samples []float32, sampleRate int, err error) {
	if len(bytes) < 12 || string(bytes[0:4]) != "RIFF" || string(bytes[8:12]) != "WAVE" {
		return nil, 0, errors.New("Not a WAV file.")
	}
	type format struct{ encoding, channels, sampleRate, bits int }
	var found *format
	var data []byte
	for at := 12; at+8 <= len(bytes); {
		id := string(bytes[at : at+4])
		size := int(binary.LittleEndian.Uint32(bytes[at+4:]))
		body := bytes[at+8 : min(len(bytes), at+8+size)]
		if id == "fmt " && len(body) >= 16 {
			tag := int(binary.LittleEndian.Uint16(body[0:]))
			encoding := tag
			if tag == wavExtensible && len(body) >= 26 {
				encoding = int(binary.LittleEndian.Uint16(body[24:]))
			}
			found = &format{encoding, int(binary.LittleEndian.Uint16(body[2:])), int(binary.LittleEndian.Uint32(body[4:])), int(binary.LittleEndian.Uint16(body[14:]))}
		} else if id == "data" {
			data = body
		}
		at += 8 + size + size%2
	}
	if found == nil || data == nil {
		return nil, 0, errors.New("The WAV file has no sound.")
	}
	pcm := found.encoding == wavPCM && found.bits == 16
	if !(pcm || (found.encoding == wavFloat && found.bits == 32)) || found.channels < 1 || found.sampleRate < 1 {
		return nil, 0, errors.New("The WAV file's format can't be read.")
	}
	width := found.bits / 8
	frames := len(data) / (width * found.channels)
	samples = make([]float32, frames)
	for frame := range frames {
		sum := 0.0
		for channel := range found.channels {
			at := (frame*found.channels + channel) * width
			if pcm {
				sum += float64(int16(binary.LittleEndian.Uint16(data[at:]))) / 32_768
			} else {
				sum += float64(math.Float32frombits(binary.LittleEndian.Uint32(data[at:])))
			}
		}
		samples[frame] = float32(sum / float64(found.channels))
	}
	return samples, found.sampleRate, nil
}

// jsRound is Math.round: halves go up, toward positive infinity.
func jsRound(value float64) float64 {
	return math.Floor(value + 0.5)
}
