package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponseModelComparisonAndLog(t *testing.T) {
	for _, tc := range []struct {
		name     string
		models   []string
		returned string
		mismatch bool
	}{
		{name: "absent", models: []string{"", "  "}},
		{name: "requested model", models: []string{"requested"}, returned: "requested"},
		{name: "mapped model", models: []string{"mapped"}, returned: "mapped"},
		{name: "different model", models: []string{"other"}, returned: "other", mismatch: true},
		{name: "case differs", models: []string{"Requested"}, returned: "Requested"},
		{name: "requested prefix", models: []string{"requested-2026-09-01"}, returned: "requested-2026-09-01"},
		{name: "mapped case differs", models: []string{"MAPPED"}, returned: "MAPPED"},
		{name: "mapped prefix", models: []string{"mapped-2026-09-01"}, returned: "mapped-2026-09-01"},
		{name: "reverse prefix still warns", models: []string{"request"}, returned: "request", mismatch: true},
		{name: "suffix", models: []string{"other-requested"}, returned: "other-requested"},
		{name: "provider path", models: []string{"vendor/requested"}, returned: "vendor/requested"},
		{name: "provider path case differs", models: []string{"vendor/REQUESTED"}, returned: "vendor/REQUESTED"},
		{name: "provider path with dated mapped model", models: []string{"vendor/mapped-2026-09-01"}, returned: "vendor/mapped-2026-09-01", mismatch: true},
		{name: "nested provider path", models: []string{"accounts/vendor/models/requested"}, returned: "accounts/vendor/models/requested"},
		{name: "provider path reverse prefix still warns", models: []string{"vendor/request"}, returned: "vendor/request", mismatch: true},
		{name: "provider path other model still warns", models: []string{"vendor/other"}, returned: "vendor/other", mismatch: true},
		{name: "provider path only still warns", models: []string{"vendor/"}, returned: "vendor/", mismatch: true},
		{name: "compatible difference survives matching frames", models: []string{"mapped", "Requested", "", "requested"}, returned: "Requested"},
		{name: "warning supersedes compatible difference", models: []string{"Requested", "other"}, returned: "other", mismatch: true},
		{name: "compatible difference cannot erase warning", models: []string{"other", "Requested"}, returned: "other", mismatch: true},
		{name: "mismatch survives later frames", models: []string{"mapped", "other", "", "requested"}, returned: "other", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{
				OriginModelName: "requested",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "mapped", IsModelMapped: true},
			}
			for _, name := range tc.models {
				info.ObserveResponseModel(name)
			}
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0, 0, 1)
			var stored struct {
				AdminInfo struct {
					ResponseModel *relaycommon.ResponseModel `json:"response_model"`
				} `json:"admin_info"`
			}
			require.NoError(t, common.UnmarshalJsonStr(other.JSONString(), &stored))
			assert.NotContains(t, other.Snapshot(), "response_model")
			if tc.returned == "" {
				assert.Nil(t, stored.AdminInfo.ResponseModel)
				return
			}
			require.NotNil(t, stored.AdminInfo.ResponseModel)
			assert.Equal(t, &relaycommon.ResponseModel{
				RequestedModel: "requested", UpstreamModel: "mapped", ReturnedModel: tc.returned,
			}, stored.AdminInfo.ResponseModel)
			assert.NotContains(t, other.JSONString(), "mismatch")
			assert.Equal(t, tc.mismatch, stored.AdminInfo.ResponseModel.Mismatch())
			assert.Equal(t, "mapped", info.UpstreamModelName)
		})
	}
}

func TestResponseModelLogOmitsUnchangedModel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream string
		returned string
		mapped   bool
		record   bool
	}{
		{name: "same model", upstream: "requested", returned: "requested"},
		{name: "no upstream name", returned: "requested"},
		{name: "mapped model", upstream: "mapped", returned: "mapped", mapped: true, record: true},
		{name: "mapped response echoes request", upstream: "mapped", returned: "requested", mapped: true, record: true},
		{name: "prefix difference", upstream: "requested", returned: "requested-2026-09-01", record: true},
		{name: "case difference", upstream: "requested", returned: "REQUESTED", record: true},
		{name: "mismatch", upstream: "requested", returned: "other", record: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{
				OriginModelName: "requested",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: tc.upstream, IsModelMapped: tc.mapped},
			}
			info.ObserveResponseModel(tc.returned)
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0, 0, 1)
			require.NotNil(t, info.ResponseModel)
			assert.Equal(t, float64(1), other.Snapshot()["model_ratio"])
			adminInfo := other.Snapshot()["admin_info"].(map[string]any)
			assert.NotContains(t, other.Snapshot(), "response_model")
			if tc.record {
				assert.Equal(t, *info.ResponseModel, adminInfo["response_model"])
			} else {
				assert.NotContains(t, adminInfo, "response_model")
			}
		})
	}
}

func TestResponseModelEmptyExpectedNamesDoNotMatchEveryPrefix(t *testing.T) {
	for _, tc := range []struct{ requested, upstream string }{
		{}, {upstream: "mapped"}, {requested: "requested"},
	} {
		t.Run(tc.requested+"/"+tc.upstream, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				OriginModelName: tc.requested,
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: tc.upstream},
			}
			info.ObserveResponseModel("other")
			require.NotNil(t, info.ResponseModel)
			assert.True(t, info.ResponseModel.Mismatch())
		})
	}
}

func TestResponseModelExpectedProviderPath(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested string
		upstream  string
		returned  string
		mismatch  bool
	}{
		{name: "requested carries provider path", requested: "vendor/requested", returned: "requested", mismatch: true},
		{name: "same provider path on both sides", requested: "vendor/requested", returned: "vendor/requested-2026-09-01"},
		{name: "provider path alone does not match", requested: "vendor/requested", returned: "vendor", mismatch: true},
		{name: "different model behind same provider still warns", requested: "vendor/requested", returned: "vendor/other", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{
				OriginModelName: tc.requested,
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: tc.upstream},
			}
			info.ObserveResponseModel(tc.returned)
			require.NotNil(t, info.ResponseModel)
			assert.Equal(t, tc.returned, info.ResponseModel.ReturnedModel)
			assert.Equal(t, tc.mismatch, info.ResponseModel.Mismatch())
		})
	}
}

func TestResponseModelRetryResetsObservation(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "requested")
	info := &relaycommon.RelayInfo{OriginModelName: "requested"}
	info.InitChannelMeta(c)
	info.ObserveResponseModel("other")
	require.True(t, info.ResponseModel.Mismatch())
	info.InitChannelMeta(c)
	assert.Nil(t, info.ResponseModel)
	info.ObserveResponseModel("requested")
	assert.False(t, info.ResponseModel.Mismatch())
}

func TestResponseModelHandlersCaptureBeforeConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	chat := `{"id":"chat_1","model":"returned","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
	chatStream := "data: " + `{"id":"chat_1","model":"returned","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}` + "\n\ndata: " + `{"id":"chat_1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}` + "\n\ndata: [DONE]\n\n"
	responses := `{"id":"resp_1","model":"returned","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`
	responsesStream := "data: " + `{"type":"response.created","response":{"id":"resp_1","model":"returned"}}` + "\n\ndata: " + `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}` + "\n\n"
	claudeBody := `{"id":"msg_1","type":"message","model":"returned","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`
	claudeStream := "data: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","model":"returned","role":"assistant","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}` + "\n\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\ndata: " + `{"type":"message_stop"}` + "\n\n"
	geminiBody := `{"modelVersion":"returned","candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`
	geminiStream := "data: " + geminiBody + "\n\n"
	for _, tc := range []struct {
		name    string
		body    string
		format  types.RelayFormat
		stream  bool
		handler func(*gin.Context, *relaycommon.RelayInfo, *http.Response) (*dto.Usage, *types.NewAPIError)
	}{
		{"chat", chat, types.RelayFormatOpenAI, false, openai.OpenaiHandler},
		{"chat stream", chatStream, types.RelayFormatOpenAI, true, openai.OaiStreamHandler},
		{"chat to claude", chat, types.RelayFormatClaude, false, openai.OpenaiHandler},
		{"chat to claude stream", chatStream, types.RelayFormatClaude, true, openai.OaiStreamHandler},
		{"chat to gemini", chat, types.RelayFormatGemini, false, openai.OpenaiHandler},
		{"chat to gemini stream", chatStream, types.RelayFormatGemini, true, openai.OaiStreamHandler},
		{"responses", responses, types.RelayFormatOpenAIResponses, false, openai.OaiResponsesHandler},
		{"responses stream", responsesStream, types.RelayFormatOpenAIResponses, true, openai.OaiResponsesStreamHandler},
		{"responses to chat", responses, types.RelayFormatOpenAI, false, openai.OaiResponsesToChatHandler},
		{"responses to chat stream", responsesStream, types.RelayFormatOpenAI, true, openai.OaiResponsesToChatStreamHandler},
		{"responses buffered to chat", responsesStream, types.RelayFormatOpenAI, false, openai.OaiResponsesToChatBufferedStreamHandler},
		{"chat to responses", chat, types.RelayFormatOpenAIResponses, false, openai.OaiChatToResponsesHandler},
		{"chat to responses stream", chatStream, types.RelayFormatOpenAIResponses, true, openai.OaiChatToResponsesStreamHandler},
		{"claude", claudeBody, types.RelayFormatClaude, false, func(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
			return claude.ClaudeHandler(c, resp, info)
		}},
		{"claude stream", claudeStream, types.RelayFormatClaude, true, func(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
			return claude.ClaudeStreamHandler(c, resp, info)
		}},
		{"claude to responses stream", claudeStream, types.RelayFormatOpenAIResponses, true, func(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
			return claude.ClaudeResponsesStreamHandler(c, resp, info)
		}},
		{"gemini", geminiBody, types.RelayFormatGemini, false, gemini.GeminiTextGenerationHandler},
		{"gemini stream", geminiStream, types.RelayFormatGemini, true, gemini.GeminiTextGenerationStreamHandler},
		{"gemini to chat", geminiBody, types.RelayFormatOpenAI, false, gemini.GeminiChatHandler},
		{"gemini to responses", geminiBody, types.RelayFormatOpenAIResponses, false, gemini.GeminiResponsesHandler},
		{"gemini to responses stream", geminiStream, types.RelayFormatOpenAIResponses, true, gemini.GeminiResponsesStreamHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			finish := helper.PreserveResponseModel(c, "requested")
			info := &relaycommon.RelayInfo{
				OriginModelName: "requested", RelayFormat: tc.format, RelayMode: relayconstant.RelayModeChatCompletions,
				IsStream: tc.stream, DisablePing: true, StartTime: time.Now(),
				ChannelMeta:         &relaycommon.ChannelMeta{UpstreamModelName: "mapped"},
				ThinkingContentInfo: relaycommon.ThinkingContentInfo{IsFirstThinkingContent: true},
			}
			usage, apiErr := tc.handler(c, info, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))})
			finish()
			require.Nil(t, apiErr)
			assert.NotContains(t, recorder.Body.String(), `"returned"`)
			assert.NotContains(t, recorder.Body.String(), `"mapped"`)
			if !strings.HasPrefix(tc.name, "chat to gemini") {
				assert.Contains(t, recorder.Body.String(), `"requested"`)
			}
			require.NotNil(t, usage)
			assert.Equal(t, 5, usage.TotalTokens)
			require.NotNil(t, info.ResponseModel)
			assert.Equal(t, &relaycommon.ResponseModel{RequestedModel: "requested", UpstreamModel: "mapped", ReturnedModel: "returned"}, info.ResponseModel)
			assert.True(t, info.ResponseModel.Mismatch())
			other := service.GenerateTextOtherInfo(c, info, 1, 1, 1, 0, 0, 0, 1)
			assert.Equal(t, *info.ResponseModel, other.Snapshot()["admin_info"].(map[string]any)["response_model"])
		})
	}
}

func TestRestoreClientResponseModel(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"chat", `{"model":"mapped","choices":[{"message":{"content":"mapped","model":"tool-data"}}],"large":9007199254740993}`, `{"model":"requested","choices":[{"message":{"content":"mapped","model":"tool-data"}}],"large":9007199254740993}`},
		{"responses event", `{"type":"response.completed","response":{"model":"mapped","output":[{"model":"tool-data"}]}}`, `{"type":"response.completed","response":{"model":"requested","output":[{"model":"tool-data"}]}}`},
		{"claude event", `{"type":"message_start","message":{"model":"mapped"}}`, `{"type":"message_start","message":{"model":"requested"}}`},
		{"gemini", `{"modelVersion":"mapped","candidates":[]}`, `{"modelVersion":"requested","candidates":[]}`},
		{"realtime", `{"type":"session.created","session":{"model":"mapped","tools":[{"model":"tool-data"}]}}`, `{"type":"session.created","session":{"model":"requested","tools":[{"model":"tool-data"}]}}`},
		{"no model", `{"usage":{"total_tokens":5}}`, `{"usage":{"total_tokens":5}}`},
		{"unrelated nested fields", `{"message":{"model":"tool-data"},"response":{"model":"tool-data"}}`, `{"message":{"model":"tool-data"},"response":{"model":"tool-data"}}`},
		{"null model", `{"model":null}`, `{"model":null}`},
		{"done", `[DONE]`, `[DONE]`},
		{"malformed", `{"model":"mapped"`, `{"model":"mapped"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			got, err := relaycommon.RestoreResponseModel(body, "requested")
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
			assert.Equal(t, tc.body, string(body), "upstream audit/usage bytes must not be mutated")
			unchanged, err := relaycommon.RestoreResponseModel(body, "")
			require.NoError(t, err)
			assert.Equal(t, tc.body, string(unchanged))
		})
	}
}

func TestResponseModelRealtimeEgress(t *testing.T) {
	events := []string{
		`{"type":"session.created","session":{"id":"session_1","model":"mapped"}}`,
		`{"type":"response.created","response":{"id":"response_1","model":"mapped"}}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer ws.Close()
		for _, event := range events {
			if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(event))) {
				return
			}
		}
		_, _, _ = ws.ReadMessage()
	}))
	defer upstream.Close()
	finished := make(chan struct{})
	engine := gin.New()
	engine.GET("/v1/realtime", func(c *gin.Context) {
		defer close(finished)
		client, err := (&websocket.Upgrader{}).Upgrade(c.Writer, c.Request, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer client.Close()
		target, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
		if !assert.NoError(t, err) {
			return
		}
		defer target.Close()
		info := &relaycommon.RelayInfo{
			OriginModelName: "requested", ClientWs: client, TargetWs: target,
			ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "mapped"},
		}
		apiErr, _ := openai.OpenaiRealtimeHandler(c, info)
		assert.Nil(t, apiErr)
		assert.Equal(t, "mapped", info.UpstreamModelName)
	})
	gateway := httptest.NewServer(engine)
	defer gateway.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/realtime", nil)
	require.NoError(t, err)
	defer client.Close()
	require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
	for _, event := range events {
		_, data, err := client.ReadMessage()
		require.NoError(t, err)
		assert.JSONEq(t, strings.ReplaceAll(event, `"mapped"`, `"requested"`), string(data))
	}
	require.NoError(t, client.Close())
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("realtime relay did not finish")
	}
}

func TestResponseModelWriter(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		chunks            []string
		want              string
	}{
		{"json fragments", "application/json", []string{`{"model":"map`, `ped","content":"mapped"}`}, `{"model":"requested","content":"mapped"}`},
		{"without content type", "", []string{`{"model":"mapped"}`}, `{"model":"requested"}`},
		{"case insensitive content type", "Application/JSON; charset=utf-8", []string{`{"model":"mapped"}`}, `{"model":"requested"}`},
		{"sse fragments", "text/event-stream", []string{"event: response.created\r\n", "data: {\"type\":\"response.created\",\"response\":{\"model\":\"map", "ped\"}}\r\n\r\n: PING\n\ndata: [DONE]\n\n"}, "event: response.created\r\ndata: {\"type\":\"response.created\",\"response\":{\"model\":\"requested\"}}\r\n\r\n: PING\n\ndata: [DONE]\n\n"},
		{"multiline sse", "text/event-stream", []string{"id: 1\ndata: {\ndata: \"model\":\"mapped\"}\n\n"}, "id: 1\ndata: {\ndata: \"model\":\"requested\"}\n\n"},
		{"binary", "audio/mpeg", []string{"\x00\xff", `{"model":"mapped"}`}, "\x00\xff" + `{"model":"mapped"}`},
		{"plain text", "text/plain", []string{`{"model":"mapped"}`}, `{"model":"mapped"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Header("Content-Type", tc.contentType)
			finish := helper.PreserveResponseModel(c, "requested")
			if tc.contentType == "application/json" {
				c.Header("Content-Length", strconv.Itoa(len(strings.Join(tc.chunks, ""))))
			}
			for i, chunk := range tc.chunks {
				var n int
				var err error
				if i%2 == 0 {
					n, err = c.Writer.Write([]byte(chunk))
				} else {
					n, err = c.Writer.WriteString(chunk)
				}
				require.NoError(t, err)
				assert.Equal(t, len(chunk), n)
			}
			require.NoError(t, helper.FlushWriter(c))
			finish()
			assert.Equal(t, tc.want, recorder.Body.String())
			if tc.contentType == "application/json" {
				assert.Equal(t, strconv.Itoa(len(tc.want)), recorder.Result().Header.Get("Content-Length"))
			}
		})
	}
}

func TestResponseModelSharedResponsesAccumulator(t *testing.T) {
	// WebSocket turns and HTTP SSE use this same accumulator. A model-free
	// terminal must not erase the model declared before an interrupted stream.
	info := &relaycommon.RelayInfo{OriginModelName: "requested", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "mapped"}}
	accumulator := service.NewResponsesUsageAccumulator(info)
	accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.created", Response: &dto.OpenAIResponsesResponse{Model: "returned"}}, nil)
	accumulator.Observe(&dto.ResponsesStreamResponse{Type: "response.failed", Response: &dto.OpenAIResponsesResponse{Usage: &dto.Usage{InputTokens: 2, OutputTokens: 3}}}, nil)
	assert.Equal(t, 5, accumulator.Finish().TotalTokens)
	require.NotNil(t, info.ResponseModel)
	assert.True(t, info.ResponseModel.Mismatch())
	assert.Equal(t, "returned", info.ResponseModel.ReturnedModel)
}

func TestResponseModelDoesNotRecordSynthesizedModel(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		OriginModelName: "requested", RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "mapped"},
	}
	_, apiErr := openai.OaiResponsesToChatBufferedStreamHandler(c, info, &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	})
	require.Nil(t, apiErr)
	assert.Nil(t, info.ResponseModel)
}
