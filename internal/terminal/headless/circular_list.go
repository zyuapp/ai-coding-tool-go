package headless

// circularList is xterm.js's ring of lines: pushing onto a full list drops the oldest.
type circularList struct {
	array      []*Line
	startIndex int
	length     int
	maxLength  int
}

func newCircularList(maxLength int) *circularList {
	return &circularList{array: make([]*Line, maxLength), maxLength: maxLength}
}

func (c *circularList) setMaxLength(newMaxLength int) {
	if c.maxLength == newMaxLength {
		return
	}
	newArray := make([]*Line, newMaxLength)
	for i := 0; i < min(newMaxLength, c.length); i++ {
		newArray[i] = c.array[c.cyclicIndex(i)]
	}
	c.array = newArray
	c.maxLength = newMaxLength
	c.startIndex = 0
}

func (c *circularList) setLength(newLength int) {
	if newLength > c.length {
		for i := c.length; i < newLength; i++ {
			c.array[c.cyclicIndex(i)] = nil
		}
	}
	c.length = newLength
}

// get and set compute the slot as JavaScript does: an index before the ring's start whose
// remainder is negative names no slot, while one whose remainder is -0 names slot 0.
func (c *circularList) get(index int) *Line {
	if c.maxLength == 0 {
		return nil
	}
	slot := (c.startIndex + index) % c.maxLength
	if slot < 0 {
		return nil
	}
	return c.array[slot]
}

func (c *circularList) set(index int, value *Line) {
	if c.maxLength == 0 {
		return
	}
	if slot := (c.startIndex + index) % c.maxLength; slot >= 0 {
		c.array[slot] = value
	}
}

func (c *circularList) push(value *Line) {
	c.array[c.cyclicIndex(c.length)] = value
	if c.length == c.maxLength {
		c.startIndex = (c.startIndex + 1) % c.maxLength
	} else {
		c.length++
	}
}

// recycle rotates the oldest line to the end and returns it for reuse. The list must be full.
func (c *circularList) recycle() *Line {
	c.startIndex = (c.startIndex + 1) % c.maxLength
	return c.array[c.cyclicIndex(c.length-1)]
}

func (c *circularList) isFull() bool { return c.length == c.maxLength }

func (c *circularList) pop() *Line {
	c.length--
	return c.array[c.cyclicIndex(c.length)]
}

func (c *circularList) splice(start, deleteCount int, items ...*Line) {
	if deleteCount > 0 {
		for i := start; i < c.length-deleteCount; i++ {
			c.array[c.cyclicIndex(i)] = c.array[c.cyclicIndex(i+deleteCount)]
		}
		c.length -= deleteCount
	}
	for i := c.length - 1; i >= start; i-- {
		c.array[c.cyclicIndex(i+len(items))] = c.array[c.cyclicIndex(i)]
	}
	for i, item := range items {
		c.array[c.cyclicIndex(start+i)] = item
	}
	if c.length+len(items) > c.maxLength {
		countToTrim := c.length + len(items) - c.maxLength
		c.startIndex += countToTrim
		c.length = c.maxLength
	} else {
		c.length += len(items)
	}
}

func (c *circularList) trimStart(count int) {
	if count > c.length {
		count = c.length
	}
	c.startIndex += count
	c.length -= count
}

func (c *circularList) shiftElements(start, count, offset int) {
	if count <= 0 || start < 0 || start >= c.length || start+offset < 0 {
		return
	}
	if offset > 0 {
		for i := count - 1; i >= 0; i-- {
			c.set(start+i+offset, c.get(start+i))
		}
		if expandListBy := start + count + offset - c.length; expandListBy > 0 {
			c.length += expandListBy
			for c.length > c.maxLength {
				c.length--
				c.startIndex++
			}
		}
	} else {
		for i := 0; i < count; i++ {
			c.set(start+i+offset, c.get(start+i))
		}
	}
}

func (c *circularList) cyclicIndex(index int) int { return (c.startIndex + index) % c.maxLength }
