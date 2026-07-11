package channel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type overloadAdmissionTestAdaptor struct {
	requestURL string
	headerErr  error
}

func (a *overloadAdmissionTestAdaptor) Init(*relaycommon.RelayInfo) {}
func (a *overloadAdmissionTestAdaptor) GetRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.requestURL, nil
}
func (a *overloadAdmissionTestAdaptor) SetupRequestHeader(*gin.Context, *http.Header, *relaycommon.RelayInfo) error {
	return a.headerErr
}
func (a *overloadAdmissionTestAdaptor) ConvertOpenAIRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeneralOpenAIRequest) (any, error) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) ConvertRerankRequest(*gin.Context, int, dto.RerankRequest) (any, error) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) ConvertEmbeddingRequest(*gin.Context, *relaycommon.RelayInfo, dto.EmbeddingRequest) (any, error) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) ConvertAudioRequest(*gin.Context, *relaycommon.RelayInfo, dto.AudioRequest) (io.Reader, error) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) ConvertImageRequest(*gin.Context, *relaycommon.RelayInfo, dto.ImageRequest) (any, error) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) ConvertOpenAIResponsesRequest(*gin.Context, *relaycommon.RelayInfo, dto.OpenAIResponsesRequest) (any, error) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) DoRequest(*gin.Context, *relaycommon.RelayInfo, io.Reader) (any, error) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) DoResponse(*gin.Context, *http.Response, *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) GetModelList() []string { return nil }
func (a *overloadAdmissionTestAdaptor) GetChannelName() string { return "test" }
func (a *overloadAdmissionTestAdaptor) ConvertClaudeRequest(*gin.Context, *relaycommon.RelayInfo, *dto.ClaudeRequest) (any, error) {
	return nil, nil
}
func (a *overloadAdmissionTestAdaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	return nil, nil
}

func overloadAdmissionTestContext(t *testing.T) (*gin.Context, *model.Channel) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	service.InitHttpClient()
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{}`))
	channel := &model.Channel{Id: 960001, ChannelInfo: model.ChannelInfo{
		ChannelOverloadProtection: model.OverloadProtection{Enabled: true, RequestsPerMinute: 1},
	}}
	lease, scope, err := service.AcquireChannelOverloadLease(ctx.Request.Context(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Empty(t, scope)
	service.SetChannelOverloadLease(ctx, lease)
	return ctx, channel
}

func TestDoApiRequestDoesNotCountSetupFailure(t *testing.T) {
	ctx, channel := overloadAdmissionTestContext(t)
	adaptor := &overloadAdmissionTestAdaptor{requestURL: "http://example.test", headerErr: errors.New("header failed")}

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	_, err := DoApiRequest(adaptor, ctx, info, bytes.NewBufferString(`{}`))
	require.Error(t, err)
	service.ReleaseChannelOverloadLease(ctx)

	next, scope, err := service.AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Empty(t, scope)
	next.Release(context.Background())
}

func TestDoApiRequestCountsActualUpstreamAttempt(t *testing.T) {
	ctx, channel := overloadAdmissionTestContext(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	adaptor := &overloadAdmissionTestAdaptor{requestURL: upstream.URL}

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	response, err := DoApiRequest(adaptor, ctx, info, bytes.NewBufferString(`{}`))
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NoError(t, response.Body.Close())
	service.ReleaseChannelOverloadLease(ctx)

	blocked, scope, err := service.AcquireChannelOverloadLease(context.Background(), channel, 0)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Equal(t, service.OverloadScopeChannel, scope)
}

func TestControllerOwnedDoApiRequestKeepsConcurrentLeaseUntilResponseFinishes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRedisEnabled := common.RedisEnabled
	common.RedisEnabled = false
	service.InitHttpClient()
	t.Cleanup(func() { common.RedisEnabled = originalRedisEnabled })

	for testIndex, contentType := range []string{"application/json", "text/event-stream"} {
		t.Run(contentType, func(t *testing.T) {
			releaseBody := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-releaseBody
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			t.Cleanup(func() {
				select {
				case <-releaseBody:
				default:
					close(releaseBody)
				}
				upstream.Close()
			})

			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{}`))
			channel := &model.Channel{Id: 961000 + testIndex, ChannelInfo: model.ChannelInfo{
				ChannelOverloadProtection: model.OverloadProtection{Enabled: true, ConcurrentRequests: 1},
			}}
			lease, scope, err := service.AcquireChannelOverloadLease(ctx.Request.Context(), channel, 0)
			require.NoError(t, err)
			require.NotNil(t, lease)
			assert.Empty(t, scope)
			service.SetChannelOverloadLease(ctx, lease)
			service.SetChannelOverloadLeaseControllerOwned(ctx, true)

			adaptor := &overloadAdmissionTestAdaptor{requestURL: upstream.URL}
			response, err := DoApiRequest(adaptor, ctx, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}, bytes.NewBufferString(`{}`))
			require.NoError(t, err)
			require.NotNil(t, response)

			blocked, blockedScope, err := service.AcquireChannelOverloadLease(context.Background(), channel, 0)
			require.NoError(t, err)
			assert.Nil(t, blocked)
			assert.Equal(t, service.OverloadScopeChannel, blockedScope)

			close(releaseBody)
			_, err = io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			service.ReleaseChannelOverloadLease(ctx)

			next, nextScope, acquireErr := service.AcquireChannelOverloadLease(context.Background(), channel, 0)
			require.NoError(t, acquireErr)
			require.NotNil(t, next)
			assert.Empty(t, nextScope)
			next.Release(context.Background())
		})
	}
}

var _ Adaptor = (*overloadAdmissionTestAdaptor)(nil)
