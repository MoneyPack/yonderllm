package workspace

import (
	"io"
	"strings"
	"testing"
)

type countingReader struct{ read int }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.read += len(p)
	return len(p), nil
}

func TestReadLimitedStopsGrowingInput(t *testing.T) {
	r := &countingReader{}
	data, err := readLimited(r)
	if err == nil || data != nil || r.read != maxFileBytes+1 {
		t.Fatalf("unbounded read: bytes=%d error=%v", r.read, err)
	}
	data, err = readLimited(strings.NewReader(strings.Repeat("x", maxFileBytes)))
	if err != nil || len(data) != maxFileBytes {
		t.Fatalf("exact boundary rejected: %v", err)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestReadLimitedPreservesErrors(t *testing.T) {
	if data, err := readLimited(failedReader{}); err != io.ErrUnexpectedEOF || data != nil {
		t.Fatalf("data=%v err=%v", data, err)
	}
}
