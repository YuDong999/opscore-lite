package db

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

const testDriverAgentStderrTailMaxBytes = 64 << 10

func TestMySQLAgentStderrKeepsBoundedLatestDiagnostics(t *testing.T) {
	client := &mysqlAgentClient{}
	oldLine := "old-" + strings.Repeat("x", 1024)
	latestLine := "最新诊断-连接已断开"
	input := strings.Repeat(oldLine+"\n", 128) + latestLine + "\n"

	client.captureStderr(strings.NewReader(input))
	got := client.stderrText()

	if len(got) > testDriverAgentStderrTailMaxBytes {
		t.Fatalf("stderr tail grew beyond %d bytes: got %d", testDriverAgentStderrTailMaxBytes, len(got))
	}
	if !strings.HasSuffix(got, latestLine) {
		t.Fatalf("stderr tail lost latest diagnostic: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("stderr tail is not valid UTF-8: %q", got)
	}
}

func TestOptionalDriverAgentStderrKeepsBoundedLatestDiagnostics(t *testing.T) {
	client := &optionalDriverAgentClient{driver: "sqlite"}
	oldLine := "旧诊断-" + strings.Repeat("y", 1024)
	latestLine := "latest-driver-diagnostic"
	input := strings.Repeat(oldLine+"\n", 128) + latestLine + "\n"

	client.captureStderr(strings.NewReader(input))
	got := client.stderrText()

	if len(got) > testDriverAgentStderrTailMaxBytes {
		t.Fatalf("stderr tail grew beyond %d bytes: got %d", testDriverAgentStderrTailMaxBytes, len(got))
	}
	if !strings.HasSuffix(got, latestLine) {
		t.Fatalf("stderr tail lost latest diagnostic: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("stderr tail is not valid UTF-8: %q", got)
	}
}

func TestDriverAgentStderrSkipsEmptyLinesAndPreservesSeparators(t *testing.T) {
	client := &mysqlAgentClient{}
	client.captureStderr(strings.NewReader("  first diagnostic  \n\n  \t\nsecond diagnostic\n"))

	if got, want := client.stderrText(), "first diagnostic | second diagnostic"; got != want {
		t.Fatalf("stderr text = %q, want %q", got, want)
	}
}

func TestDriverAgentStderrBoundsOversizedUTF8Line(t *testing.T) {
	client := &mysqlAgentClient{}
	latestSuffix := "最终诊断"
	line := strings.Repeat("界", testDriverAgentStderrTailMaxBytes) + latestSuffix

	client.captureStderr(strings.NewReader(line + "\n"))
	got := client.stderrText()

	if len(got) > testDriverAgentStderrTailMaxBytes {
		t.Fatalf("oversized stderr line grew beyond %d bytes: got %d", testDriverAgentStderrTailMaxBytes, len(got))
	}
	if !strings.HasSuffix(got, latestSuffix) {
		t.Fatalf("oversized stderr line lost latest suffix: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("oversized stderr line was split inside UTF-8: %q", got)
	}
}

func TestDriverAgentStderrSanitizesInvalidUTF8(t *testing.T) {
	client := &mysqlAgentClient{}
	input := append([]byte("valid diagnostic\ninvalid-"), 0xff, 0xfe)
	input = append(input, []byte("-latest\n")...)

	client.captureStderr(strings.NewReader(string(input)))
	got := client.stderrText()

	if !utf8.ValidString(got) {
		t.Fatalf("stderr text retained invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "invalid-") || !strings.HasSuffix(got, "-latest") {
		t.Fatalf("stderr text lost diagnostic context while sanitizing: %q", got)
	}
}

func TestDriverAgentStderrBoundsManyTinyDiagnostics(t *testing.T) {
	client := &mysqlAgentClient{}
	latestLine := "latest"
	input := strings.Repeat("x\n", driverAgentStderrMaxEntries+1024) + latestLine + "\n"

	client.captureStderr(strings.NewReader(input))
	got := client.stderrText()

	if len(got) > testDriverAgentStderrTailMaxBytes {
		t.Fatalf("tiny stderr diagnostics grew beyond %d bytes: got %d", testDriverAgentStderrTailMaxBytes, len(got))
	}
	if !strings.HasSuffix(got, latestLine) {
		t.Fatalf("stderr tail lost latest tiny diagnostic: %q", got)
	}
}

func TestBoundedDiagnosticTailPreservesWrappedEntryBoundaries(t *testing.T) {
	var tail boundedDiagnosticTail
	expected := make([]string, 0, 512)
	expectedBytes := 0

	for index := 0; index < 2000; index++ {
		line := strings.Repeat(string(rune('a'+index%26)), 37+index%181) + " | embedded-" + strconv.Itoa(index)
		for len(expected) > 0 && expectedBytes+len(driverAgentStderrSeparator)+len(line) > testDriverAgentStderrTailMaxBytes {
			expectedBytes -= len(expected[0])
			expected = expected[1:]
			if len(expected) > 0 {
				expectedBytes -= len(driverAgentStderrSeparator)
			}
		}
		if len(expected) > 0 {
			expectedBytes += len(driverAgentStderrSeparator)
		}
		expected = append(expected, line)
		expectedBytes += len(line)
		tail.Append(line)
	}

	if got, want := tail.String(), strings.Join(expected, driverAgentStderrSeparator); got != want {
		t.Fatalf("wrapped stderr tail differs from bounded reference: got %d bytes, want %d bytes", len(got), len(want))
	}
}
