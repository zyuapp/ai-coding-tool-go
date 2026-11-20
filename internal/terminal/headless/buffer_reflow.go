package headless

func (b *Buffer) reflow(newCols, newRows int) {
	if b.cols == newCols {
		return
	}
	if newCols > b.cols {
		b.reflowLarger(newCols, newRows)
	} else {
		b.reflowSmaller(newCols, newRows)
	}
}

func (b *Buffer) reflowLarger(newCols, newRows int) {
	toRemove := reflowLargerGetLinesToRemove(b.lines, b.cols, newCols, b.ybase+b.y, nullCell(attributeData{}))
	if len(toRemove) == 0 {
		return
	}
	layout, countRemoved := reflowLargerCreateNewLayout(b.lines, toRemove)
	reflowLargerApplyNewLayout(b.lines, layout)
	b.reflowLargerAdjustViewport(newCols, newRows, countRemoved)
}

func (b *Buffer) reflowLargerAdjustViewport(newCols, newRows, countRemoved int) {
	fill := nullCell(attributeData{})
	for adjustments := countRemoved; adjustments > 0; adjustments-- {
		if b.ybase == 0 {
			if b.y > 0 {
				b.y--
			}
			if b.lines.length < newRows {
				b.lines.push(newLine(newCols, fill, false))
			}
		} else {
			b.ybase--
		}
	}
	b.savedY = max(b.savedY-countRemoved, 0)
}

type pendingInsert struct {
	start    int
	newLines []*Line
}

func (b *Buffer) reflowSmaller(newCols, newRows int) {
	fill := nullCell(attributeData{})
	var toInsert []pendingInsert
	countToInsert := 0
	for y := b.lines.length - 1; y >= 0; y-- {
		nextLine := b.lines.get(y)
		if nextLine == nil || !nextLine.isWrapped && nextLine.getTrimmedLength() <= newCols {
			continue
		}
		wrappedLines := []*Line{nextLine}
		for nextLine.isWrapped && y > 0 {
			y--
			nextLine = b.lines.get(y)
			wrappedLines = append([]*Line{nextLine}, wrappedLines...)
		}
		absoluteY := b.ybase + b.y
		if absoluteY >= y && absoluteY < y+len(wrappedLines) {
			continue
		}
		lastLineLength := wrappedLines[len(wrappedLines)-1].getTrimmedLength()
		destLineLengths := reflowSmallerGetNewLineLengths(wrappedLines, b.cols, newCols)
		linesToAdd := len(destLineLengths) - len(wrappedLines)
		var trimmedLines int
		if b.ybase == 0 && b.y != b.lines.length-1 {
			trimmedLines = max(0, b.y-b.lines.maxLength+linesToAdd)
		} else {
			trimmedLines = max(0, b.lines.length-b.lines.maxLength+linesToAdd)
		}
		var newLines []*Line
		for i := 0; i < linesToAdd; i++ {
			newLines = append(newLines, b.blankLine(attributeData{}, true))
		}
		if len(newLines) > 0 {
			toInsert = append(toInsert, pendingInsert{start: y + len(wrappedLines) + countToInsert, newLines: newLines})
			countToInsert += len(newLines)
		}
		wrappedLines = append(wrappedLines, newLines...)
		destLineIndex := len(destLineLengths) - 1
		destCol := destLineLengths[destLineIndex]
		if destCol == 0 {
			destLineIndex--
			destCol = lengthAt(destLineLengths, destLineIndex)
		}
		srcLineIndex := len(wrappedLines) - linesToAdd - 1
		srcCol := lastLineLength
		for srcLineIndex >= 0 {
			cellsToCopy := min(srcCol, destCol)
			if destLineIndex < 0 || destLineIndex >= len(wrappedLines) {
				break
			}
			wrappedLines[destLineIndex].copyCellsFrom(wrappedLines[srcLineIndex], srcCol-cellsToCopy, destCol-cellsToCopy, cellsToCopy, true)
			destCol -= cellsToCopy
			if destCol == 0 {
				destLineIndex--
				destCol = lengthAt(destLineLengths, destLineIndex)
			}
			srcCol -= cellsToCopy
			if srcCol == 0 {
				srcLineIndex--
				srcCol = getWrappedLineTrimmedLength(wrappedLines, max(srcLineIndex, 0), b.cols)
			}
		}
		for i := range wrappedLines {
			if i < len(destLineLengths) && destLineLengths[i] < newCols {
				wrappedLines[i].setCell(destLineLengths[i], fill)
			}
		}
		for adjustments := linesToAdd - trimmedLines; adjustments > 0; adjustments-- {
			if b.ybase == 0 {
				if b.y < newRows-1 {
					b.y++
					b.lines.pop()
				} else {
					b.ybase++
				}
			} else if b.ybase < min(b.lines.maxLength, b.lines.length+countToInsert)-newRows {
				b.ybase++
			}
		}
		b.savedY = min(b.savedY+linesToAdd, b.ybase+newRows-1)
	}
	if len(toInsert) == 0 {
		return
	}
	originalLines := make([]*Line, b.lines.length)
	for i := range originalLines {
		originalLines[i] = b.lines.get(i)
	}
	originalLinesLength := b.lines.length
	originalLineIndex := originalLinesLength - 1
	nextToInsertIndex := 0
	b.lines.setLength(min(b.lines.maxLength, b.lines.length+countToInsert))
	countInsertedSoFar := 0
	for i := min(b.lines.maxLength-1, originalLinesLength+countToInsert-1); i >= 0; i-- {
		if nextToInsertIndex < len(toInsert) && toInsert[nextToInsertIndex].start > originalLineIndex+countInsertedSoFar {
			next := toInsert[nextToInsertIndex]
			for nextI := len(next.newLines) - 1; nextI >= 0; nextI-- {
				b.lines.set(i, next.newLines[nextI])
				i--
			}
			i++
			countInsertedSoFar += len(next.newLines)
			nextToInsertIndex++
		} else {
			b.lines.set(i, originalLines[originalLineIndex])
			originalLineIndex--
		}
	}
}

// lengthAt reads a line length the way JavaScript reads past an array's end: as nothing.
func lengthAt(lengths []int, index int) int {
	if index < 0 || index >= len(lengths) {
		return 0
	}
	return lengths[index]
}

func reflowLargerGetLinesToRemove(lines *circularList, oldCols, newCols, bufferAbsoluteY int, fill Cell) []int {
	var toRemove []int
	for y := 0; y < lines.length-1; y++ {
		i := y + 1
		nextLine := lines.get(i)
		if !nextLine.isWrapped {
			continue
		}
		wrappedLines := []*Line{lines.get(y)}
		for i < lines.length && nextLine.isWrapped {
			wrappedLines = append(wrappedLines, nextLine)
			i++
			nextLine = lines.get(i)
			if nextLine == nil {
				break
			}
		}
		if bufferAbsoluteY >= y && bufferAbsoluteY < i {
			y += len(wrappedLines) - 1
			continue
		}
		destLineIndex := 0
		destCol := getWrappedLineTrimmedLength(wrappedLines, destLineIndex, oldCols)
		srcLineIndex := 1
		srcCol := 0
		for srcLineIndex < len(wrappedLines) {
			srcTrimmedLineLength := getWrappedLineTrimmedLength(wrappedLines, srcLineIndex, oldCols)
			srcRemainingCells := srcTrimmedLineLength - srcCol
			destRemainingCells := newCols - destCol
			cellsToCopy := min(srcRemainingCells, destRemainingCells)
			wrappedLines[destLineIndex].copyCellsFrom(wrappedLines[srcLineIndex], srcCol, destCol, cellsToCopy, false)
			destCol += cellsToCopy
			if destCol == newCols {
				destLineIndex++
				destCol = 0
			}
			srcCol += cellsToCopy
			if srcCol == srcTrimmedLineLength {
				srcLineIndex++
				srcCol = 0
			}
			if destCol == 0 && destLineIndex != 0 {
				if wrappedLines[destLineIndex-1].getWidth(newCols-1) == 2 {
					wrappedLines[destLineIndex].copyCellsFrom(wrappedLines[destLineIndex-1], newCols-1, destCol, 1, false)
					destCol++
					wrappedLines[destLineIndex-1].setCell(newCols-1, fill)
				}
			}
		}
		wrappedLines[destLineIndex].replaceCells(destCol, newCols, fill, false)
		countToRemove := 0
		for k := len(wrappedLines) - 1; k > 0; k-- {
			if k > destLineIndex || wrappedLines[k].getTrimmedLength() == 0 {
				countToRemove++
			} else {
				break
			}
		}
		if countToRemove > 0 {
			toRemove = append(toRemove, y+len(wrappedLines)-countToRemove, countToRemove)
		}
		y += len(wrappedLines) - 1
	}
	return toRemove
}

func reflowLargerCreateNewLayout(lines *circularList, toRemove []int) ([]int, int) {
	var layout []int
	nextToRemoveIndex := 0
	nextToRemoveStart := toRemove[nextToRemoveIndex]
	countRemovedSoFar := 0
	for i := 0; i < lines.length; i++ {
		if nextToRemoveStart == i {
			nextToRemoveIndex++
			countToRemove := toRemove[nextToRemoveIndex]
			i += countToRemove - 1
			countRemovedSoFar += countToRemove
			nextToRemoveIndex++
			if nextToRemoveIndex < len(toRemove) {
				nextToRemoveStart = toRemove[nextToRemoveIndex]
			} else {
				nextToRemoveStart = -1
			}
		} else {
			layout = append(layout, i)
		}
	}
	return layout, countRemovedSoFar
}

func reflowLargerApplyNewLayout(lines *circularList, layout []int) {
	newLayoutLines := make([]*Line, len(layout))
	for i, index := range layout {
		newLayoutLines[i] = lines.get(index)
	}
	for i, line := range newLayoutLines {
		lines.set(i, line)
	}
	lines.setLength(len(layout))
}

func reflowSmallerGetNewLineLengths(wrappedLines []*Line, oldCols, newCols int) []int {
	var newLineLengths []int
	cellsNeeded := 0
	for i := range wrappedLines {
		cellsNeeded += getWrappedLineTrimmedLength(wrappedLines, i, oldCols)
	}
	srcCol, srcLine, cellsAvailable := 0, 0, 0
	for cellsAvailable < cellsNeeded {
		if cellsNeeded-cellsAvailable < newCols {
			newLineLengths = append(newLineLengths, cellsNeeded-cellsAvailable)
			break
		}
		srcCol += newCols
		oldTrimmedLength := getWrappedLineTrimmedLength(wrappedLines, srcLine, oldCols)
		if srcCol > oldTrimmedLength {
			srcCol -= oldTrimmedLength
			srcLine++
		}
		endsWithWide := srcCol >= 1 && srcLine < len(wrappedLines) && wrappedLines[srcLine].getWidth(srcCol-1) == 2
		if endsWithWide {
			srcCol--
		}
		lineLength := newCols
		if endsWithWide {
			lineLength = newCols - 1
		}
		newLineLengths = append(newLineLengths, lineLength)
		cellsAvailable += lineLength
	}
	return newLineLengths
}

func getWrappedLineTrimmedLength(lines []*Line, i, cols int) int {
	if i == len(lines)-1 {
		return lines[i].getTrimmedLength()
	}
	endsInNull := !lines[i].hasContent(cols-1) && lines[i].getWidth(cols-1) == 1
	followingLineStartsWithWide := lines[i+1].getWidth(0) == 2
	if endsInNull && followingLineStartsWithWide {
		return cols - 1
	}
	return cols
}
