package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func responseModelInfo(format types.RelayFormat) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RequestedModelName: "foo", OriginModelName: "routing-foo", RelayFormat: format,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "foo-v1", IsModelMapped: true,
			ChannelOtherSettings: dto.ChannelOtherSettings{ResponseModelMapping: &dto.ResponseModelMappingSettings{
				Enabled: true, Mapping: map[string]string{"foo": "public-foo", "public-foo": "not-chained", "bar": "public-bar"},
			}},
		},
	}
}

func TestResponseModelMappingSelection(t *testing.T) {
	info := responseModelInfo(types.RelayFormatOpenAI)
	assert.Equal(t, "public-foo", info.DownstreamModelName("foo-v1"))
	info.RequestedModelName = "bar"
	assert.Equal(t, "public-bar", info.DownstreamModelName("bar-v1"))
	info.RequestedModelName = "unlisted"
	assert.Equal(t, "unlisted", info.DownstreamModelName("unlisted-v1"))
	info.ChannelOtherSettings.ResponseModelMapping.Enabled = false
	assert.Equal(t, "routing-foo", info.DownstreamModelName("foo-v1"), "legacy mapping remains active")
	info.IsModelMapped = false
	assert.Equal(t, "foo-v1", info.DownstreamModelName("foo-v1"))
	info.ChannelOtherSettings.ResponseModelMapping.Enabled = true
	info.RequestedModelName = "foo"
	info.ChannelOtherSettings.ResponseModelMapping.Mapping = nil
	assert.Equal(t, "foo", info.DownstreamModelName("foo-v1"))
	info.ChannelOtherSettings.ResponseModelMapping.Mapping = map[string]string{"foo": "foo"}
	assert.Equal(t, "foo", info.DownstreamModelName("foo-v1"))
	assert.Equal(t, "routing-foo", info.OriginModelName)
	assert.Equal(t, "foo-v1", info.UpstreamModelName)
}

func TestResponseModelMappingProtocolMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format types.RelayFormat
		body   string
		path   string
	}{
		{"chat", types.RelayFormatOpenAI, `{"model":"foo-v1","choices":[{"message":{"content":"foo-v1","model":"content-model"}}],"extension":{"model":"keep"},"usage":{"total_tokens":7}}`, "model"},
		{"responses", types.RelayFormatOpenAIResponses, `{"type":"response.completed","response":{"model":"foo-v1","usage":{"total_tokens":7}},"extension":{"model":"keep"}}`, "response.model"},
		{"compact", types.RelayFormatOpenAIResponsesCompaction, `{"model":"foo-v1","extension":{"model":"keep"}}`, "model"},
		{"claude", types.RelayFormatClaude, `{"type":"message_start","message":{"model":"foo-v1","content":[]},"extension":{"model":"keep"}}`, "message.model"},
		{"gemini", types.RelayFormatGemini, `{"modelVersion":"foo-v1","candidates":[],"extension":{"model":"keep"}}`, "modelVersion"},
		{"gemini array", types.RelayFormatGemini, `[{"modelVersion":"foo-v1","candidates":[]}]`, "0.modelVersion"},
		{"embedding", types.RelayFormatEmbedding, `{"model":"foo-v1","data":[{"embedding":[1,2]}]}`, "model"},
		{"rerank", types.RelayFormatRerank, `{"model":"foo-v1","results":[{"index":0,"relevance_score":0.9}]}`, "model"},
		{"image", types.RelayFormatOpenAIImage, `{"model":"foo-v1","data":[{"b64_json":"aGVsbG8="}]}`, "model"},
		{"audio", types.RelayFormatOpenAIAudio, `{"model":"foo-v1","text":"foo-v1"}`, "model"},
		{"realtime session", types.RelayFormatOpenAIRealtime, `{"type":"session.created","session":{"model":"foo-v1"}}`, "session.model"},
		{"realtime response", types.RelayFormatOpenAIRealtime, `{"type":"response.done","response":{"model":"foo-v1"}}`, "response.model"},
		{"task", types.RelayFormatTask, `{"data":{"data":{"model":"foo-v1"},"properties":{"origin_model_name":"foo"}}}`, "data.data.model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := responseModelInfo(tc.format)
			patched := info.RewriteResponseModelJSON([]byte(tc.body))
			assert.Equal(t, "public-foo", gjson.GetBytes(patched, tc.path).String())
			// Restoring the single model leaf must reproduce all original bytes.
			assert.Equal(t, tc.body, strings.Replace(string(patched), `"public-foo"`, `"foo-v1"`, 1))
		})
	}
	info := responseModelInfo(types.RelayFormatOpenAIResponses)
	for _, body := range []string{
		`{"choices":[],"extension":{"model":"keep"}}`,
		`{"model":null}`, `{"model":42}`, `{"model":"foo-v1","error":{"message":"failed"}}`,
		`{"type":"response.failed","response":{"model":"foo-v1"}}`, `[DONE]`, `not-json`,
	} {
		assert.Equal(t, body, string(info.RewriteResponseModelJSON([]byte(body))))
	}
}

func TestResponseModelMappingHTTPDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		status            int
		parts             []string
		want              string
	}{
		{"JSON split write", "application/json", 200, []string{`{"model":"foo-`, `v1","content":"keep"}`}, `{"model":"public-foo","content":"keep"}`},
		{"SSE split event", "text/event-stream", 200, []string{"event: response.created\ndata: {\"response\":{\"model\":\"foo-", "v1\"}}\n", "\ndata: [DONE]\n\n"}, "event: response.created\ndata: {\"response\":{\"model\":\"public-foo\"}}\n\ndata: [DONE]\n\n"},
		{"SSE CRLF", "text/event-stream", 200, []string{"id: 1\r\nevent: response.created\r\ndata: {\"response\":{\"model\":\"foo-v1\"}}\r\n\r\n: ping\r\n\r\n"}, "id: 1\r\nevent: response.created\r\ndata: {\"response\":{\"model\":\"public-foo\"}}\r\n\r\n: ping\r\n\r\n"},
		{"SSE multiline data", "text/event-stream", 200, []string{"data: {\"model\":\n", "data: \"foo-v1\"}\n\n"}, "data: {\"model\":\ndata: \"public-foo\"}\n:\n\n"},
		{"binary", "audio/mpeg", 200, []string{`{"model":"foo-v1"}`, "\x00\xff"}, "{\"model\":\"foo-v1\"}\x00\xff"},
		{"error", "application/json", 400, []string{`{"model":"foo-v1","error":"invalid"}`}, `{"model":"foo-v1","error":"invalid"}`},
		{"plain transcription", "text/plain", 200, []string{`{"model":"foo-v1"}`}, `{"model":"foo-v1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			original := c.Writer
			w := relaycommon.WrapResponseModelWriter(c, responseModelInfo(types.RelayFormatOpenAIResponses))
			c.Header("Content-Type", tc.contentType)
			c.Header("Content-Length", "999")
			c.Status(tc.status)
			for _, part := range tc.parts {
				n, err := c.Writer.WriteString(part)
				require.NoError(t, err)
				assert.Equal(t, len(part), n)
				c.Writer.Flush()
			}
			require.NoError(t, w.Finish())
			assert.Same(t, original, c.Writer)
			assert.Equal(t, tc.status, rr.Code)
			assert.Equal(t, tc.want, rr.Body.String())
			if tc.name == "binary" || tc.name == "error" || tc.name == "plain transcription" {
				assert.Equal(t, "999", rr.Header().Get("Content-Length"))
			} else {
				assert.Empty(t, rr.Header().Get("Content-Length"))
			}
		})
	}
}

func TestResponseModelMappingChatHandler(t *testing.T) {
	info := responseModelInfo(types.RelayFormatOpenAI)
	info.IsModelMapped = false // Upstream adds a suffix even without request mapping.
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(common.RequestIdKey, "response-model-regression")
	w := relaycommon.WrapResponseModelWriter(c, info)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"model":"foo-v1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5},"extension":{"model":"keep"}}`))}
	usage, apiErr := openai.OpenaiHandler(c, info, resp)
	require.Nil(t, apiErr)
	require.NoError(t, w.Finish())
	assert.Equal(t, "public-foo", gjson.Get(rr.Body.String(), "model").String())
	assert.Equal(t, "keep", gjson.Get(rr.Body.String(), "extension.model").String())
	assert.Equal(t, 5, usage.TotalTokens)
	assert.Equal(t, "foo-v1", info.UpstreamModelName)
}

type responseModelFailWriter struct{ gin.ResponseWriter }

func (w responseModelFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestResponseModelMappingDeliveryFailure(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Writer = responseModelFailWriter{c.Writer}
	w := relaycommon.WrapResponseModelWriter(c, responseModelInfo(types.RelayFormatOpenAI))
	_, err := c.Writer.WriteString(`{"model":"foo-v1"}`)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.ErrorIs(t, w.Finish(), io.ErrClosedPipe)
}

func TestResponseModelMappingRealtimeDelivery(t *testing.T) {
	upgrader := websocket.Upgrader{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.created","session":{"model":"foo-v1","input_audio_format":"pcm16"}}`))
		_, _, _ = conn.ReadMessage() // Wait for the bridge to close after client disconnect.
	}))
	defer upstream.Close()
	finished := make(chan struct{})
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		client, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer client.Close()
		target, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
		if err != nil {
			return
		}
		defer target.Close()
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		info := responseModelInfo(types.RelayFormatOpenAIRealtime)
		info.ClientWs, info.TargetWs = client, target
		_, _ = openai.OpenaiRealtimeHandler(c, info)
	}))
	defer bridge.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(bridge.URL, "http"), nil)
	require.NoError(t, err)
	defer client.Close()
	if deadline, ok := t.Deadline(); ok {
		require.NoError(t, client.SetReadDeadline(deadline))
	}
	_, message, err := client.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "public-foo", gjson.GetBytes(message, "session.model").String())
	assert.Equal(t, "pcm16", gjson.GetBytes(message, "session.input_audio_format").String())
	require.NoError(t, client.Close())
	<-finished
}

// RESPONSE_MODEL_TEST_DSN selects a dedicated MySQL/PostgreSQL test database.
// Without it, the same contract runs against a fresh real SQLite database.
func TestResponseModelMappingPersistence(t *testing.T) {
	oldDB, oldLogDB := model.DB, model.LOG_DB
	oldPath, oldMaster := common.SQLitePath, common.IsMasterNode
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	t.Setenv("SQL_DSN", os.Getenv("RESPONSE_MODEL_TEST_DSN"))
	common.SQLitePath, common.IsMasterNode = t.TempDir()+"/response-model.db", false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLogDB
		common.SQLitePath, common.IsMasterNode = oldPath, oldMaster
		common.SetDatabaseTypes(oldMainType, oldLogType)
	})
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}))
	var version string
	query := "SELECT version()"
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		query = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("database=%s version=%s", common.MainDatabaseType(), version)
	channel := &model.Channel{Name: "response-model-regression", Type: constant.ChannelTypeOpenAI, Key: "test-only", OtherSettings: `{"disable_video_understanding":true}`}
	require.NoError(t, channel.ValidateSettings())
	require.NoError(t, db.Create(channel).Error)
	t.Cleanup(func() { db.Delete(&model.Task{}, "channel_id = ?", channel.Id); db.Delete(channel) })
	assert.Nil(t, channel.GetOtherSettings().ResponseModelMapping)
	for _, invalid := range []string{`{"mapping":{"foo":null}}`, `{"mapping":{"foo":7}}`, `{"mapping":{"":"bar"}}`, `{"mapping":{"foo":" "}}`, `{"mapping":[]}`} {
		bad := &model.Channel{OtherSettings: `{"response_model_mapping":` + invalid + `}`}
		require.Error(t, bad.ValidateSettings(), invalid)
	}
	setting := channel.GetOtherSettings()
	setting.ResponseModelMapping = responseModelInfo(types.RelayFormatTask).ChannelOtherSettings.ResponseModelMapping
	channel.SetOtherSettings(setting)
	require.NoError(t, channel.ValidateSettings())
	require.NoError(t, db.Model(channel).Update("settings", channel.OtherSettings).Error)
	info := responseModelInfo(types.RelayFormatTask)
	info.ChannelId = channel.Id
	task := model.InitTask(constant.TaskPlatform("response-model-test"), info)
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Task{}))
	loaded, err := model.GetChannelById(channel.Id, false)
	require.NoError(t, err)
	assert.True(t, loaded.GetOtherSettings().DisableVideoUnderstanding)
	assert.Equal(t, setting.ResponseModelMapping, loaded.GetOtherSettings().ResponseModelMapping)
	var savedTask model.Task
	require.NoError(t, db.First(&savedTask, task.ID).Error)
	assert.Equal(t, "foo", savedTask.PrivateData.RequestedModelName)
	assert.Equal(t, "public-foo", service.TaskResponseModelInfo(&savedTask).ResponseModelName())
	assert.Equal(t, "routing-foo", savedTask.Properties.OriginModelName)
	data, err := service.RewriteTaskResponseModels(map[string]any{"data": []any{map[string]any{"id": savedTask.TaskID, "model": "foo-v1", "content": map[string]any{"model": "keep"}}}}, []*model.Task{&savedTask})
	require.NoError(t, err)
	assert.Equal(t, "public-foo", gjson.GetBytes(data, "data.0.model").String())
	assert.Equal(t, "keep", gjson.GetBytes(data, "data.0.content.model").String())
	legacyTask := savedTask
	legacyTask.PrivateData.RequestedModelName = ""
	assert.Equal(t, "routing-foo", service.TaskResponseModelInfo(&legacyTask).ResponseModelName())
	secondChannel := &model.Channel{Name: "second-response-model-channel", Type: constant.ChannelTypeOpenAI, Key: "test-only", OtherSettings: `{"response_model_mapping":{"enabled":true,"mapping":{"foo":"second-channel"}}}`}
	require.NoError(t, db.Create(secondChannel).Error)
	t.Cleanup(func() { db.Delete(secondChannel) })
	secondTask := savedTask
	secondTask.TaskID, secondTask.ChannelId = "task_second", secondChannel.Id
	data, err = service.RewriteTaskResponseModels(map[string]any{"data": []any{
		map[string]any{"id": savedTask.TaskID, "model": "foo-v1"},
		map[string]any{"id": secondTask.TaskID, "model": "foo-v1"},
		map[string]any{"id": "unknown-task", "model": "keep"},
	}}, []*model.Task{&savedTask, &secondTask})
	require.NoError(t, err)
	assert.Equal(t, "public-foo", gjson.GetBytes(data, "data.0.model").String())
	assert.Equal(t, "second-channel", gjson.GetBytes(data, "data.1.model").String())
	assert.Equal(t, "keep", gjson.GetBytes(data, "data.2.model").String())
	setting.ResponseModelMapping.Mapping["foo"] = "updated-name"
	channel.SetOtherSettings(setting)
	require.NoError(t, db.Model(channel).Update("settings", channel.OtherSettings).Error)
	assert.Equal(t, "updated-name", service.TaskResponseModelInfo(&savedTask).ResponseModelName())
	setting.ResponseModelMapping.Enabled = false
	channel.SetOtherSettings(setting)
	require.NoError(t, db.Model(channel).Update("settings", channel.OtherSettings).Error)
	assert.Empty(t, service.TaskResponseModelInfo(&savedTask).ResponseModelName())
	loaded, err = model.GetChannelById(channel.Id, false)
	require.NoError(t, err)
	assert.Equal(t, "updated-name", loaded.GetOtherSettings().ResponseModelMapping.Mapping["foo"])
}
