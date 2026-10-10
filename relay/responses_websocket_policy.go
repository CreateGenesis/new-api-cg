package relay

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	appmodel "github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Responses WebSocket messages use the same output-policy and usage recorders
// as HTTP SSE. The bridge changes only the transport framing; terminal events
// remain withheld until the request middleware and billing have finished.
type responsesWSStreamWriter struct {
	gin.ResponseWriter
	header  http.Header
	pending bytes.Buffer
	info    *relaycommon.RelayInfo
	state   *responsesWSCallState
	send    func(int, []byte) error
	status  int
	size    int
	written bool
}

func (w *responsesWSStreamWriter) Header() http.Header { return w.header }
func (w *responsesWSStreamWriter) Status() int         { return w.status }
func (w *responsesWSStreamWriter) Size() int           { return w.size }
func (w *responsesWSStreamWriter) Written() bool       { return w.written }
func (w *responsesWSStreamWriter) Flush()              {}
func (w *responsesWSStreamWriter) WriteHeaderNow()     { w.written = true }
func (w *responsesWSStreamWriter) WriteHeader(code int) {
	if !w.written {
		w.status = code
		w.written = true
	}
}

func (w *responsesWSStreamWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *responsesWSStreamWriter) Write(data []byte) (int, error) {
	w.written = true
	w.pending.Write(data)
	for {
		index, length := simulatedModelCacheSSEBoundary(w.pending.Bytes())
		if index < 0 {
			break
		}
		frame := w.pending.Next(index + length)
		payload, ok := simulatedModelCacheSSEData(frame)
		if !ok || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		payload = w.info.RewriteResponseModelJSON(payload)
		var event struct {
			Type string `json:"type"`
		}
		_ = common.Unmarshal(payload, &event)
		switch event.Type {
		case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled", "error":
			w.state.terminal = &responsesWSMessage{kind: websocket.TextMessage, body: bytes.Clone(payload)}
		default:
			if err := w.send(websocket.TextMessage, payload); err != nil {
				return 0, err
			}
		}
	}
	w.size += len(data)
	return len(data), nil
}

// Admission is per response.create, rather than per persistent connection.
// A locked connection cannot rotate a key or channel without a new handshake.
func admitResponsesWSChannel(c *gin.Context, info *relaycommon.RelayInfo, retry *service.RetryParam, channel *appmodel.Channel, locked bool) (*appmodel.Channel, *types.NewAPIError) {
	_, pinned, _ := service.GetChannelConstraints(c).ResolvedPin()
	keyAttempts := 0
	for {
		eligible := service.ChannelAcceptsRequestMode(channel, types.RequestModeStream) && channel.MatchesInputTokenRouting(retry.InputTokenEstimates)
		if !eligible {
			if pinned || locked {
				return channel, types.NewErrorWithStatusCode(errors.New("connection channel does not accept this streaming request or input token count"), types.ErrorCodeGetChannelFailed, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
			}
		} else if pinned {
			return channel, nil
		} else {
			lease, scope, err := service.AcquireChannelOverloadLease(c.Request.Context(), channel, common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex))
			if err != nil {
				return channel, types.NewErrorWithStatusCode(err, types.ErrorCodeChannelOverloaded, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
			}
			if scope == "" {
				service.SetChannelOverloadLease(c, lease)
				return channel, nil
			}
			if locked {
				return channel, types.NewErrorWithStatusCode(errors.New("connection channel is overloaded"), types.ErrorCodeChannelOverloaded, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
			}
			if scope == service.OverloadScopeMultiKey && keyAttempts < len(channel.GetKeys()) {
				keyAttempts++
				c.Set("overload_key_selection", true)
				common.SetContextKey(c, constant.ContextKeyChannelMultiKeyOverload, true)
				setupErr := middleware.SetupContextForSelectedChannel(c, channel, info.UpstreamAttemptModelName())
				c.Set("overload_key_selection", false)
				common.SetContextKey(c, constant.ContextKeyChannelMultiKeyOverload, false)
				if setupErr == nil {
					info.InitChannelMeta(c)
					continue
				}
			}
		}
		retry.ExcludeUnavailableChannel(channel)
		service.ClearRequestChannelAffinitySelection(c)
		var apiErr *types.NewAPIError
		channel, apiErr = selectResponsesWSChannel(c, info.UpstreamAttemptModelName(), retry)
		if apiErr != nil {
			return channel, apiErr
		}
		info.InitChannelMeta(c)
		keyAttempts = 0
	}
}

func finishResponsesWSOutput(c *gin.Context, info *relaycommon.RelayInfo, attempt *simulatedModelCacheAttempt, recorder *simulatedModelCacheRecorder, usage *dto.Usage, terminal *responsesWSMessage) *types.NewAPIError {
	if terminal != nil {
		var event dto.ResponsesStreamResponse
		_ = common.Unmarshal(terminal.body, &event)
		if err := helper.ResponseChunkData(c, event, string(terminal.body)); err != nil && c.Request.Context().Err() == nil {
			if outputErr := finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage); outputErr != nil {
				return outputErr
			}
			return types.NewError(err, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
		}
	}
	if apiErr := finishSimulatedModelCacheRecorder(c, info, attempt, recorder, usage); apiErr != nil {
		return apiErr
	}
	ConsumeResponsesQuota(c, info, usage)
	return nil
}

func releaseResponsesWSOverload(c *gin.Context) {
	service.ReleaseChannelOverloadLease(c)
	if value, exists := c.Get("websocket_overload_lease"); exists {
		if lease, ok := value.(*service.OverloadLease); ok && lease != nil {
			lease.Release(context.WithoutCancel(c.Request.Context()))
		}
		c.Set("websocket_overload_lease", (*service.OverloadLease)(nil))
	}
}
