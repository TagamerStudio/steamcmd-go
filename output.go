package steamcmd

import (
	"bytes"
	"sync"
)

type boundedOutput struct {
	mu        sync.Mutex
	data      bytes.Buffer
	limit     int64
	truncated bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - int64(w.data.Len())
	if remaining > 0 {
		n := int64(len(p))
		if n > remaining {
			n = remaining
		}
		_, _ = w.data.Write(p[:n])
		if n < int64(len(p)) {
			w.truncated = true
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func (w *boundedOutput) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.data.Bytes()...)
}

func boundBytes(data []byte, limit int64) ([]byte, bool) {
	if int64(len(data)) <= limit {
		return append([]byte(nil), data...), false
	}
	return append([]byte(nil), data[:limit]...), true
}
