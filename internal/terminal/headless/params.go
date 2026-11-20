package headless

const (
	paramMaxValue    = 0x7FFFFFFF
	paramMaxLength   = 32
	subParamsMaxSize = 32
)

// params holds a CSI's numeric parameters and their colon-separated sub-parameters.
type params struct {
	values          [paramMaxLength]int32
	length          int
	subParams       [subParamsMaxSize]int32
	subParamsLength int
	subParamsIdx    [paramMaxLength]uint16
	rejectDigits    bool
	rejectSubDigits bool
	digitIsSub      bool
}

func (p *params) reset() {
	p.length = 0
	p.subParamsLength = 0
	p.rejectDigits = false
	p.rejectSubDigits = false
	p.digitIsSub = false
}

func (p *params) addParam(value int32) {
	p.digitIsSub = false
	if p.length >= paramMaxLength {
		p.rejectDigits = true
		return
	}
	p.subParamsIdx[p.length] = uint16(p.subParamsLength<<8 | p.subParamsLength)
	p.values[p.length] = value
	p.length++
}

func (p *params) addSubParam(value int32) {
	p.digitIsSub = true
	if p.length == 0 {
		return
	}
	if p.rejectDigits || p.subParamsLength >= subParamsMaxSize {
		p.rejectSubDigits = true
		return
	}
	p.subParams[p.subParamsLength] = value
	p.subParamsLength++
	p.subParamsIdx[p.length-1]++
}

func (p *params) hasSubParams(index int) bool {
	idx := p.subParamsIdx[index]
	return int(idx&0xFF)-int(idx>>8) > 0
}

func (p *params) getSubParams(index int) []int32 {
	idx := p.subParamsIdx[index]
	start, end := int(idx>>8), int(idx&0xFF)
	if end-start > 0 {
		return p.subParams[start:end]
	}
	return nil
}

func (p *params) addDigit(value int32) {
	length := p.length
	if p.digitIsSub {
		length = p.subParamsLength
	}
	if p.rejectDigits || length == 0 || p.digitIsSub && p.rejectSubDigits {
		return
	}
	store := p.values[:]
	if p.digitIsSub {
		store = p.subParams[:]
	}
	current := store[length-1]
	if current != -1 {
		store[length-1] = int32(min(int64(current)*10+int64(value), paramMaxValue))
	} else {
		store[length-1] = value
	}
}

// at reads a parameter slot as xterm.js reads its typed array: slots past length keep older values.
func (p *params) at(index int) int {
	if index < 0 || index >= paramMaxLength {
		return 0
	}
	return int(p.values[index])
}
