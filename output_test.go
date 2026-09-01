package steamcmd

import (
	"bytes"
	"testing"
)

func TestBoundedOutput(t *testing.T) {
	output := boundedOutput{limit: 10}
	if n, err := output.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("initial Write() = %d, %v", n, err)
	}
	if got := output.Bytes(); !bytes.Equal(got, []byte("abc")) {
		t.Fatalf("Bytes() = %q", got)
	}
	got := output.Bytes()
	got[0] = 'x'
	if string(output.Bytes()) != "abc" {
		t.Fatal("Bytes() exposed internal storage")
	}

	output = boundedOutput{limit: 5}
	if n, err := output.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("partial Write() = %d, %v", n, err)
	}
	if n, err := output.Write([]byte("g")); err != nil || n != 1 {
		t.Fatalf("full Write() = %d, %v", n, err)
	}
	if !output.truncated || string(output.Bytes()) != "abcde" {
		t.Fatalf("bounded output = %q, truncated=%t", output.Bytes(), output.truncated)
	}

	output = boundedOutput{limit: 0}
	if n, err := output.Write(nil); err != nil || n != 0 {
		t.Fatalf("empty Write() = %d, %v", n, err)
	}
	if n, err := output.Write([]byte("x")); err != nil || n != 1 {
		t.Fatalf("zero-limit Write() = %d, %v", n, err)
	}
	if !output.truncated || len(output.Bytes()) != 0 {
		t.Fatalf("zero-limit output = %q, truncated=%t", output.Bytes(), output.truncated)
	}
}
