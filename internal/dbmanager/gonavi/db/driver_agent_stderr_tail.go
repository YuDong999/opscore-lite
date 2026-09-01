package db

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	driverAgentStderrTailMaxBytes = 64 << 10
	driverAgentStderrSeparator    = " | "
	driverAgentStderrMaxEntries   = (driverAgentStderrTailMaxBytes + len(driverAgentStderrSeparator)) /
		(1 + len(driverAgentStderrSeparator))
)

// boundedDiagnosticTail keeps complete recent diagnostics within a fixed byte
// budget. A single oversized diagnostic is represented by its UTF-8-safe tail.
type boundedDiagnosticTail struct {
	mu sync.Mutex

	data       []byte
	start      int
	length     int
	entryBytes []uint32
	entryHead  int
	entryCount int
}

func (b *boundedDiagnosticTail) Append(text string) {
	text = strings.ToValidUTF8(text, "\uFFFD")
	if text == "" {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureData()

	if len(text) >= driverAgentStderrTailMaxBytes {
		b.replaceWithSuffix(text)
		return
	}

	for b.entryCount > 0 && b.length+len(driverAgentStderrSeparator)+len(text) > driverAgentStderrTailMaxBytes {
		b.removeOldest()
	}
	if b.entryCount > 0 {
		b.appendBytes(driverAgentStderrSeparator)
	}
	b.appendBytes(text)
	b.appendEntryLength(len(text))
}

func (b *boundedDiagnosticTail) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.length == 0 {
		return ""
	}

	result := make([]byte, b.length)
	first := copy(result, b.data[b.start:])
	copy(result[first:], b.data[:b.length-first])
	return string(result)
}

func (b *boundedDiagnosticTail) ensureData() {
	if b.data == nil {
		b.data = make([]byte, driverAgentStderrTailMaxBytes)
	}
}

func (b *boundedDiagnosticTail) replaceWithSuffix(text string) {
	start := len(text) - driverAgentStderrTailMaxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	b.clear()
	b.appendBytes(text[start:])
	b.appendEntryLength(len(text) - start)
}

func (b *boundedDiagnosticTail) appendBytes(text string) {
	writeAt := (b.start + b.length) % len(b.data)
	written := copy(b.data[writeAt:], text)
	copy(b.data, text[written:])
	b.length += len(text)
}

func (b *boundedDiagnosticTail) appendEntryLength(length int) {
	if b.entryCount == len(b.entryBytes) {
		capacity := len(b.entryBytes) * 2
		if capacity < 8 {
			capacity = 8
		}
		if capacity > driverAgentStderrMaxEntries {
			capacity = driverAgentStderrMaxEntries
		}
		resized := make([]uint32, capacity)
		for index := 0; index < b.entryCount; index++ {
			resized[index] = b.entryBytes[(b.entryHead+index)%len(b.entryBytes)]
		}
		b.entryBytes = resized
		b.entryHead = 0
	}
	b.entryBytes[(b.entryHead+b.entryCount)%len(b.entryBytes)] = uint32(length)
	b.entryCount++
}

func (b *boundedDiagnosticTail) removeOldest() {
	removedBytes := int(b.entryBytes[b.entryHead])
	b.entryBytes[b.entryHead] = 0
	b.entryHead = (b.entryHead + 1) % len(b.entryBytes)
	b.entryCount--
	if b.entryCount > 0 {
		removedBytes += len(driverAgentStderrSeparator)
	}
	b.start = (b.start + removedBytes) % len(b.data)
	b.length -= removedBytes
	if b.entryCount == 0 {
		b.start = 0
		b.length = 0
		b.entryHead = 0
	}
}

func (b *boundedDiagnosticTail) clear() {
	for b.entryCount > 0 {
		b.entryBytes[b.entryHead] = 0
		b.entryHead = (b.entryHead + 1) % len(b.entryBytes)
		b.entryCount--
	}
	b.start = 0
	b.length = 0
	b.entryHead = 0
}
