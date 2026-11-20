package headless

// The VT500 state machine of xterm.js's EscapeSequenceParser.

const (
	stateGround = iota
	stateEscape
	stateEscapeIntermediate
	stateCsiEntry
	stateCsiParam
	stateCsiIntermediate
	stateCsiIgnore
	stateSosPmApcString
	stateOscString
	stateDcsEntry
	stateDcsParam
	stateDcsIgnore
	stateDcsIntermediate
	stateDcsPassthrough
)

const (
	actionIgnore = iota
	actionError
	actionPrint
	actionExecute
	actionOscStart
	actionOscPut
	actionOscEnd
	actionCsiDispatch
	actionParam
	actionCollect
	actionEscDispatch
	actionClear
	actionDcsHook
	actionDcsPut
	actionDcsUnhook
)

const (
	transitionActionShift = 4
	transitionStateMask   = 15
	indexStateShift       = 8
	nonASCIIPrintable     = 0xA0

	oscPayloadLimit = 10000000
)

var transitions = func() *[4095]uint8 {
	var table [4095]uint8
	set := func(code int, state, action, next int) {
		table[state<<indexStateShift|code] = uint8(action<<transitionActionShift | next)
	}
	setMany := func(codes []int, state, action, next int) {
		for _, code := range codes {
			set(code, state, action, next)
		}
	}
	r := func(start, end int) []int {
		codes := make([]int, 0, end-start)
		for i := start; i < end; i++ {
			codes = append(codes, i)
		}
		return codes
	}
	for i := range table {
		table[i] = uint8(actionError<<transitionActionShift | stateGround)
	}
	printables := r(0x20, 0x7f)
	executables := append(append(r(0x00, 0x18), 0x19), r(0x1c, 0x20)...)

	setMany(printables, stateGround, actionPrint, stateGround)
	for state := stateGround; state <= stateDcsPassthrough; state++ {
		setMany([]int{0x18, 0x1a, 0x99, 0x9a}, state, actionExecute, stateGround)
		setMany(r(0x80, 0x90), state, actionExecute, stateGround)
		setMany(r(0x90, 0x98), state, actionExecute, stateGround)
		set(0x9c, state, actionIgnore, stateGround)
		set(0x1b, state, actionClear, stateEscape)
		set(0x9d, state, actionOscStart, stateOscString)
		setMany([]int{0x98, 0x9e, 0x9f}, state, actionIgnore, stateSosPmApcString)
		set(0x9b, state, actionClear, stateCsiEntry)
		set(0x90, state, actionClear, stateDcsEntry)
	}
	setMany(executables, stateGround, actionExecute, stateGround)
	setMany(executables, stateEscape, actionExecute, stateEscape)
	set(0x7f, stateEscape, actionIgnore, stateEscape)
	setMany(executables, stateOscString, actionIgnore, stateOscString)
	setMany(executables, stateCsiEntry, actionExecute, stateCsiEntry)
	set(0x7f, stateCsiEntry, actionIgnore, stateCsiEntry)
	setMany(executables, stateCsiParam, actionExecute, stateCsiParam)
	set(0x7f, stateCsiParam, actionIgnore, stateCsiParam)
	setMany(executables, stateCsiIgnore, actionExecute, stateCsiIgnore)
	setMany(executables, stateCsiIntermediate, actionExecute, stateCsiIntermediate)
	set(0x7f, stateCsiIntermediate, actionIgnore, stateCsiIntermediate)
	setMany(executables, stateEscapeIntermediate, actionExecute, stateEscapeIntermediate)
	set(0x7f, stateEscapeIntermediate, actionIgnore, stateEscapeIntermediate)
	// osc
	set(0x5d, stateEscape, actionOscStart, stateOscString)
	setMany(printables, stateOscString, actionOscPut, stateOscString)
	set(0x7f, stateOscString, actionOscPut, stateOscString)
	setMany([]int{0x9c, 0x1b, 0x18, 0x1a, 0x07}, stateOscString, actionOscEnd, stateGround)
	setMany(r(0x1c, 0x20), stateOscString, actionIgnore, stateOscString)
	// sos/pm/apc
	setMany([]int{0x58, 0x5e, 0x5f}, stateEscape, actionIgnore, stateSosPmApcString)
	setMany(printables, stateSosPmApcString, actionIgnore, stateSosPmApcString)
	setMany(executables, stateSosPmApcString, actionIgnore, stateSosPmApcString)
	set(0x9c, stateSosPmApcString, actionIgnore, stateGround)
	set(0x7f, stateSosPmApcString, actionIgnore, stateSosPmApcString)
	// csi
	set(0x5b, stateEscape, actionClear, stateCsiEntry)
	setMany(r(0x40, 0x7f), stateCsiEntry, actionCsiDispatch, stateGround)
	setMany(r(0x30, 0x3c), stateCsiEntry, actionParam, stateCsiParam)
	setMany([]int{0x3c, 0x3d, 0x3e, 0x3f}, stateCsiEntry, actionCollect, stateCsiParam)
	setMany(r(0x30, 0x3c), stateCsiParam, actionParam, stateCsiParam)
	setMany(r(0x40, 0x7f), stateCsiParam, actionCsiDispatch, stateGround)
	setMany([]int{0x3c, 0x3d, 0x3e, 0x3f}, stateCsiParam, actionIgnore, stateCsiIgnore)
	setMany(r(0x20, 0x40), stateCsiIgnore, actionIgnore, stateCsiIgnore)
	set(0x7f, stateCsiIgnore, actionIgnore, stateCsiIgnore)
	setMany(r(0x40, 0x7f), stateCsiIgnore, actionIgnore, stateGround)
	setMany(r(0x20, 0x30), stateCsiEntry, actionCollect, stateCsiIntermediate)
	setMany(r(0x20, 0x30), stateCsiIntermediate, actionCollect, stateCsiIntermediate)
	setMany(r(0x30, 0x40), stateCsiIntermediate, actionIgnore, stateCsiIgnore)
	setMany(r(0x40, 0x7f), stateCsiIntermediate, actionCsiDispatch, stateGround)
	setMany(r(0x20, 0x30), stateCsiParam, actionCollect, stateCsiIntermediate)
	// esc
	setMany(r(0x20, 0x30), stateEscape, actionCollect, stateEscapeIntermediate)
	setMany(r(0x20, 0x30), stateEscapeIntermediate, actionCollect, stateEscapeIntermediate)
	setMany(r(0x30, 0x7f), stateEscapeIntermediate, actionEscDispatch, stateGround)
	setMany(r(0x30, 0x50), stateEscape, actionEscDispatch, stateGround)
	setMany(r(0x51, 0x58), stateEscape, actionEscDispatch, stateGround)
	setMany([]int{0x59, 0x5a, 0x5c}, stateEscape, actionEscDispatch, stateGround)
	setMany(r(0x60, 0x7f), stateEscape, actionEscDispatch, stateGround)
	// dcs
	set(0x50, stateEscape, actionClear, stateDcsEntry)
	setMany(executables, stateDcsEntry, actionIgnore, stateDcsEntry)
	set(0x7f, stateDcsEntry, actionIgnore, stateDcsEntry)
	setMany(r(0x1c, 0x20), stateDcsEntry, actionIgnore, stateDcsEntry)
	setMany(r(0x20, 0x30), stateDcsEntry, actionCollect, stateDcsIntermediate)
	setMany(r(0x30, 0x3c), stateDcsEntry, actionParam, stateDcsParam)
	setMany([]int{0x3c, 0x3d, 0x3e, 0x3f}, stateDcsEntry, actionCollect, stateDcsParam)
	setMany(executables, stateDcsIgnore, actionIgnore, stateDcsIgnore)
	setMany(r(0x20, 0x80), stateDcsIgnore, actionIgnore, stateDcsIgnore)
	setMany(r(0x1c, 0x20), stateDcsIgnore, actionIgnore, stateDcsIgnore)
	setMany(executables, stateDcsParam, actionIgnore, stateDcsParam)
	set(0x7f, stateDcsParam, actionIgnore, stateDcsParam)
	setMany(r(0x1c, 0x20), stateDcsParam, actionIgnore, stateDcsParam)
	setMany(r(0x30, 0x3c), stateDcsParam, actionParam, stateDcsParam)
	setMany([]int{0x3c, 0x3d, 0x3e, 0x3f}, stateDcsParam, actionIgnore, stateDcsIgnore)
	setMany(r(0x20, 0x30), stateDcsParam, actionCollect, stateDcsIntermediate)
	setMany(executables, stateDcsIntermediate, actionIgnore, stateDcsIntermediate)
	set(0x7f, stateDcsIntermediate, actionIgnore, stateDcsIntermediate)
	setMany(r(0x1c, 0x20), stateDcsIntermediate, actionIgnore, stateDcsIntermediate)
	setMany(r(0x20, 0x30), stateDcsIntermediate, actionCollect, stateDcsIntermediate)
	setMany(r(0x30, 0x40), stateDcsIntermediate, actionIgnore, stateDcsIgnore)
	setMany(r(0x40, 0x7f), stateDcsIntermediate, actionDcsHook, stateDcsPassthrough)
	setMany(r(0x40, 0x7f), stateDcsParam, actionDcsHook, stateDcsPassthrough)
	setMany(r(0x40, 0x7f), stateDcsEntry, actionDcsHook, stateDcsPassthrough)
	setMany(executables, stateDcsPassthrough, actionDcsPut, stateDcsPassthrough)
	setMany(printables, stateDcsPassthrough, actionDcsPut, stateDcsPassthrough)
	set(0x7f, stateDcsPassthrough, actionIgnore, stateDcsPassthrough)
	setMany([]int{0x1b, 0x9c, 0x18, 0x1a}, stateDcsPassthrough, actionDcsUnhook, stateGround)
	// unicode
	set(nonASCIIPrintable, stateGround, actionPrint, stateGround)
	set(nonASCIIPrintable, stateOscString, actionOscPut, stateOscString)
	set(nonASCIIPrintable, stateCsiIgnore, actionIgnore, stateCsiIgnore)
	set(nonASCIIPrintable, stateDcsIgnore, actionIgnore, stateDcsIgnore)
	set(nonASCIIPrintable, stateDcsPassthrough, actionDcsPut, stateDcsPassthrough)
	return &table
}()

const (
	oscStateStart = iota
	oscStateID
	oscStatePayload
	oscStateAbort
)

type parser struct {
	currentState       int
	precedingJoinState uint32
	params             params
	collect            int
	oscState           int
	oscID              int
	oscData            []uint32
	oscHitLimit        bool
}

func (p *parser) reset() {
	p.currentState = stateGround
	p.oscReset()
	p.params.reset()
	p.params.addParam(0)
	p.collect = 0
	p.precedingJoinState = 0
}

func (p *parser) oscReset() {
	p.oscState = oscStateStart
	p.oscID = -1
	p.oscData = p.oscData[:0]
	p.oscHitLimit = false
}

// handlesOsc lists the OSC commands whose payload changes what a snapshot holds.
func handlesOsc(id int) bool { return id == 0 || id == 2 || id == 8 }

func (p *parser) oscPut(data []uint32) {
	if p.oscState == oscStateAbort {
		return
	}
	start := 0
	if p.oscState == oscStateID {
		for start < len(data) {
			code := data[start]
			start++
			if code == 0x3b {
				p.oscState = oscStatePayload
				break
			}
			if code < 0x30 || code > 0x39 {
				p.oscState = oscStateAbort
				return
			}
			if p.oscID == -1 {
				p.oscID = 0
			}
			p.oscID = p.oscID*10 + int(code) - 48
		}
	}
	if p.oscState == oscStatePayload && start < len(data) && handlesOsc(p.oscID) && !p.oscHitLimit {
		p.oscData = append(p.oscData, data[start:]...)
		if len(p.oscData) > oscPayloadLimit {
			p.oscData = p.oscData[:0]
			p.oscHitLimit = true
		}
	}
}

// oscEnd returns the finished command and its payload, or -1 when there is none to run.
func (p *parser) oscEnd(success bool) (int, string) {
	defer p.oscReset()
	if p.oscState == oscStateStart || p.oscState == oscStateAbort || !success || p.oscHitLimit {
		return -1, ""
	}
	return p.oscID, utf32ToString(p.oscData)
}

func utf32ToString(data []uint32) string {
	runes := make([]rune, len(data))
	for i, code := range data {
		runes[i] = rune(code)
	}
	return string(runes)
}

func (t *Terminal) parse(data []uint32) {
	p := &t.parser
	length := len(data)
	for i := 0; i < length; i++ {
		code := data[i]
		index := code
		if index >= nonASCIIPrintable {
			index = nonASCIIPrintable
		}
		transition := int(transitions[p.currentState<<indexStateShift|int(index)])
		switch transition >> transitionActionShift {
		case actionPrint:
			j := i + 1
			for ; j < length; j++ {
				if c := data[j]; c < 0x20 || c > 0x7e && c < nonASCIIPrintable {
					break
				}
			}
			t.print(data, i, j)
			i = j - 1
		case actionExecute:
			t.execute(code)
			p.precedingJoinState = 0
		case actionCsiDispatch:
			t.csiDispatch(p.collect<<8|int(code), &p.params)
			p.precedingJoinState = 0
		case actionParam:
			for {
				switch code {
				case 0x3b:
					p.params.addParam(0)
				case 0x3a:
					p.params.addSubParam(-1)
				default:
					p.params.addDigit(int32(code) - 48)
				}
				i++
				if i >= length {
					break
				}
				code = data[i]
				if code <= 0x2f || code >= 0x3c {
					break
				}
			}
			i--
		case actionCollect:
			p.collect = p.collect<<8 | int(code)
		case actionEscDispatch:
			t.escDispatch(p.collect<<8 | int(code))
			p.precedingJoinState = 0
		case actionClear:
			p.params.reset()
			p.params.addParam(0)
			p.collect = 0
		case actionDcsPut:
			j := i + 1
			for ; j < length; j++ {
				if c := data[j]; c == 0x18 || c == 0x1a || c == 0x1b || c > 0x7f && c < nonASCIIPrintable {
					break
				}
			}
			i = j - 1
		case actionDcsUnhook:
			if code == 0x1b {
				transition |= stateEscape
			}
			p.params.reset()
			p.params.addParam(0)
			p.collect = 0
			p.precedingJoinState = 0
		case actionOscStart:
			p.oscReset()
			p.oscState = oscStateID
		case actionOscPut:
			j := i + 1
			for ; j < length; j++ {
				if c := data[j]; c < 0x20 || c > 0x7f && c < nonASCIIPrintable {
					break
				}
			}
			p.oscPut(data[i:j])
			i = j - 1
		case actionOscEnd:
			id, payload := p.oscEnd(code != 0x18 && code != 0x1a)
			if id >= 0 {
				t.oscDispatch(id, payload)
			}
			if code == 0x1b {
				transition |= stateEscape
			}
			p.params.reset()
			p.params.addParam(0)
			p.collect = 0
			p.precedingJoinState = 0
		}
		p.currentState = transition & transitionStateMask
	}
}
