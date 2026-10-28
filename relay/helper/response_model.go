package helper

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// PreserveResponseModel covers both shared helpers and adaptors writing directly
// to Gin. Install once before relay/retries, and defer the returned cleanup.
func PreserveResponseModel(c *gin.Context, requested string) func() {
	if requested == "" {
		return func() {}
	}
	writer := &responseModelWriter{ResponseWriter: c.Writer, requested: requested}
	c.Writer = writer
	return func() {
		if len(writer.pending) > 0 && writer.err == nil {
			writer.err = writer.writePayload(writer.pending)
			writer.pending = nil
		}
		if writer.err != nil {
			logger.LogError(c, "failed to write client response: "+writer.err.Error())
		}
	}
}

type responseModelWriter struct {
	gin.ResponseWriter
	requested string
	pending   []byte
	err       error
}

func (w *responseModelWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseModelWriter) Written() bool {
	return len(w.pending) > 0 || w.ResponseWriter.Written()
}

func (w *responseModelWriter) WriteHeaderNow() {
	// A caller committing headers early cannot know the rewritten body length.
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeaderNow()
}

func (w *responseModelWriter) Flush() {
	if len(w.pending) > 0 {
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.Flush()
}

func (w *responseModelWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *responseModelWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	stream := strings.HasPrefix(contentType, "text/event-stream")
	if !stream && contentType != "" && !strings.Contains(contentType, "json") {
		return w.ResponseWriter.Write(data)
	}
	w.pending = append(w.pending, data...)
	if stream {
		// Buffer one SSE event, not the stream; Write calls need not align with frames.
		for {
			end, separator := bytes.Index(w.pending, []byte("\n\n")), 2
			if crlf := bytes.Index(w.pending, []byte("\r\n\r\n")); crlf >= 0 && (end < 0 || crlf < end) {
				end, separator = crlf, 4
			}
			if end < 0 {
				break
			}
			end += separator
			w.err = w.writePayload(w.pending[:end])
			w.pending = w.pending[end:]
			if w.err != nil {
				return 0, w.err
			}
		}
	} else if gjson.ValidBytes(w.pending) {
		w.err = w.writePayload(w.pending)
		w.pending = nil
	}
	if len(w.pending) > getScannerBufferSize() {
		w.err = errors.New("client response exceeds buffer limit")
	}
	if w.err != nil {
		return 0, w.err
	}
	return len(data), nil
}

func (w *responseModelWriter) writePayload(data []byte) error {
	var rewritten []byte
	var err error
	if strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		separator := "\n"
		if bytes.Contains(data, []byte("\r\n")) {
			separator = "\r\n"
		}
		lines := strings.Split(string(data), separator)
		var payload []string
		for _, line := range lines {
			if value, ok := strings.CutPrefix(line, "data:"); ok {
				payload = append(payload, value)
			}
		}
		original := strings.Join(payload, "\n")
		rewritten, err = relaycommon.RestoreResponseModel([]byte(original), w.requested)
		if err != nil {
			return err
		}
		if string(rewritten) == original {
			rewritten = data
		} else {
			var event strings.Builder
			wroteData := false
			for i, line := range lines {
				if strings.HasPrefix(line, "data:") {
					if wroteData {
						continue
					}
					line = "data:" + strings.ReplaceAll(string(rewritten), "\n", separator+"data:")
					wroteData = true
				}
				event.WriteString(line)
				if i < len(lines)-1 {
					event.WriteString(separator)
				}
			}
			rewritten = []byte(event.String())
		}
	} else {
		rewritten, err = relaycommon.RestoreResponseModel(data, w.requested)
		if err != nil {
			return err
		}
	}
	if !w.ResponseWriter.Written() && w.Header().Get("Content-Length") != "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
	}
	n, err := w.ResponseWriter.Write(rewritten)
	if err == nil && n != len(rewritten) {
		err = io.ErrShortWrite
	}
	return err
}
