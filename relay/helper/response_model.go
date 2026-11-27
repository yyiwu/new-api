package helper

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// PreserveResponseModel covers both shared helpers and adaptors writing directly
// to Gin. Install once before relay/retries, and defer the returned cleanup.
func PreserveResponseModel(c *gin.Context, requested string) func() {
	if requested == "" {
		return func() {}
	}
	return FinalizeRelayResponse(c, requested, "", nil)
}

func FinalizeRelayResponse(c *gin.Context, requested string, format types.RelayFormat, settledUsage func() *relaycommon.UsageReceipt) func() {
	writer := &responseModelWriter{ResponseWriter: c.Writer, requested: requested, format: format, settledUsage: settledUsage}
	c.Writer = writer
	return func() {
		if err := writer.finish(); err != nil {
			logger.LogError(c, "failed to write client response: "+err.Error())
		}
	}
}

type responseModelWriter struct {
	gin.ResponseWriter
	requested    string
	format       types.RelayFormat
	settledUsage func() *relaycommon.UsageReceipt
	pending      []byte
	jsonBody     []byte
	terminal     *relaySSEEvent
	trailer      []byte
	err          error
}

func (w *responseModelWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseModelWriter) Written() bool {
	return len(w.pending) > 0 || len(w.jsonBody) > 0 || w.terminal != nil || w.ResponseWriter.Written()
}

func (w *responseModelWriter) WriteHeaderNow() {
	// A caller committing headers early cannot know the rewritten body length.
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeaderNow()
}

func (w *responseModelWriter) Flush() {
	if len(w.jsonBody) > 0 {
		return
	}
	if len(w.pending) > 0 || w.terminal != nil {
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
			w.err = w.writeEvent(w.pending[:end])
			w.pending = w.pending[end:]
			if w.err != nil {
				return 0, w.err
			}
		}
	} else if gjson.ValidBytes(w.pending) {
		if w.settledUsage != nil && w.success() && gjson.ParseBytes(w.pending).IsObject() {
			w.jsonBody = w.pending
		} else {
			w.jsonBody = nil
			w.err = w.writeJSON(w.pending, nil)
		}
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

func (w *responseModelWriter) success() bool {
	return w.Status() >= http.StatusOK && w.Status() < http.StatusMultipleChoices
}

func (w *responseModelWriter) finish() error {
	if w.err != nil {
		return w.err
	}
	var receipt *relaycommon.UsageReceipt
	if w.settledUsage != nil && w.success() {
		receipt = w.settledUsage()
	}
	pending, body := w.pending, w.jsonBody
	w.pending, w.jsonBody = nil, nil
	if strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		if len(pending) > 0 {
			if err := w.writeEvent(pending); err != nil {
				return err
			}
		}
		if w.terminal != nil {
			if err := w.writeTerminal(receipt); err != nil {
				return err
			}
			w.ResponseWriter.Flush()
		}
		return nil
	}
	if len(body) > 0 {
		return w.writeJSON(body, receipt)
	}
	if len(pending) > 0 {
		return w.writeJSON(pending, nil)
	}
	return nil
}

func (w *responseModelWriter) writeJSON(data []byte, receipt *relaycommon.UsageReceipt) error {
	path := ""
	if w.settledUsage != nil && gjson.ValidBytes(data) && gjson.ParseBytes(data).IsObject() {
		path = "usage"
		if w.format == types.RelayFormatGemini {
			path = "usageMetadata"
		}
		if responseError := gjson.GetBytes(data, "error"); responseError.Exists() && responseError.Type != gjson.Null {
			receipt = nil
		}
	}
	rewritten, err := w.rewrite(data, path, receipt)
	if err != nil {
		return err
	}
	return w.writeBody(rewritten)
}

type relaySSEEvent struct {
	raw       []byte
	separator string
	lines     []string
	payload   string
	usagePath string
}

func (w *responseModelWriter) writeEvent(data []byte) error {
	separator := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		separator = "\r\n"
	}
	event := relaySSEEvent{raw: data, separator: separator, lines: strings.Split(string(data), separator)}
	var payload []string
	for _, line := range event.lines {
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			payload = append(payload, value)
		}
	}
	event.payload = strings.Join(payload, "\n")
	if w.settledUsage != nil {
		parsed := gjson.Parse(event.payload)
		var terminal bool
		if parsed.IsObject() && gjson.Valid(event.payload) {
			event.usagePath, terminal = w.streamUsage(parsed)
		}
		if w.terminal != nil {
			marker := strings.TrimSpace(event.payload)
			choices := parsed.Get("choices")
			emptyChatChunk := w.format == types.RelayFormatOpenAI && choices.IsArray() && choices.Get("#").Int() == 0 && !terminal
			if (marker == "[DONE]" || marker == "" || parsed.Get("type").Str == "message_stop" || emptyChatChunk) &&
				len(w.terminal.raw)+len(w.trailer)+len(data) <= getScannerBufferSize() {
				rewritten, err := w.renderEvent(&event, nil)
				w.trailer = append(w.trailer, rewritten...)
				return err
			}
			if err := w.writeTerminal(nil); err != nil {
				return err
			}
		}
		if terminal && w.success() {
			w.terminal = &event
			return nil
		}
	}
	rewritten, err := w.renderEvent(&event, nil)
	if err != nil {
		return err
	}
	return w.writeBody(rewritten)
}

func (w *responseModelWriter) streamUsage(data gjson.Result) (string, bool) {
	switch w.format {
	case types.RelayFormatOpenAIResponses, types.RelayFormatOpenAIResponsesCompaction:
		if !data.Get("response").IsObject() {
			return "usage", false
		}
		terminal := relaycommon.IsResponsesTerminalEvent(data.Get("type").Str)
		return "response.usage", terminal
	case types.RelayFormatClaude:
		kind := data.Get("type").Str
		if kind == "message_start" {
			return "message.usage", false
		}
		return "usage", kind == "message_delta"
	case types.RelayFormatGemini:
		candidates := data.Get("candidates").Array()
		finished := slices.ContainsFunc(candidates, func(candidate gjson.Result) bool {
			return candidate.Get("finishReason").Str != ""
		})
		return "usageMetadata", finished || len(candidates) == 0 && data.Get("usageMetadata").IsObject()
	default:
		return "usage", data.Get("usage").IsObject()
	}
}

func (w *responseModelWriter) writeTerminal(receipt *relaycommon.UsageReceipt) error {
	rewritten, err := w.renderEvent(w.terminal, receipt)
	if err != nil {
		return err
	}
	if err := w.writeBody(rewritten); err != nil {
		return err
	}
	if len(w.trailer) > 0 {
		if err := w.writeBody(w.trailer); err != nil {
			return err
		}
	}
	w.terminal, w.trailer = nil, nil
	return nil
}

func (w *responseModelWriter) renderEvent(event *relaySSEEvent, receipt *relaycommon.UsageReceipt) ([]byte, error) {
	rewritten, err := w.rewrite([]byte(event.payload), event.usagePath, receipt)
	if err != nil {
		return nil, err
	}
	if string(rewritten) == event.payload {
		return event.raw, nil
	}
	var out strings.Builder
	wroteData := false
	for index, line := range event.lines {
		if strings.HasPrefix(line, "data:") {
			if wroteData {
				continue
			}
			line = "data:" + strings.ReplaceAll(string(rewritten), "\n", event.separator+"data:")
			wroteData = true
		}
		out.WriteString(line)
		if index < len(event.lines)-1 {
			out.WriteString(event.separator)
		}
	}
	return []byte(out.String()), nil
}

func (w *responseModelWriter) rewrite(data []byte, usagePath string, receipt *relaycommon.UsageReceipt) ([]byte, error) {
	data, err := relaycommon.RestoreResponseModel(data, w.requested)
	if err != nil || usagePath == "" {
		return data, err
	}
	return relaycommon.ApplyUsageReceipt(data, usagePath, receipt)
}

func (w *responseModelWriter) writeBody(body []byte) error {
	if !w.ResponseWriter.Written() && w.Header().Get("Content-Length") != "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	n, err := w.ResponseWriter.Write(body)
	if err == nil && n != len(body) {
		err = io.ErrShortWrite
	}
	return err
}
