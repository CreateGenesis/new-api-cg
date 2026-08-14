package claude

import (
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	openaiadapter "github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	if info.IsTNTTencentOpenAIConversion() {
		converted, err := relayconvert.ConvertTNTTencentClaudeRequest(request)
		if err != nil {
			return nil, err
		}
		if info.IsKimiK3OfficialCompatibility() {
			if len(request.ResponseFormat) > 0 {
				var responseFormat dto.ResponseFormat
				if err := common.Unmarshal(request.ResponseFormat, &responseFormat); err != nil {
					return nil, fmt.Errorf("invalid response_format: %w", err)
				}
				converted.ResponseFormat = &responseFormat
			}
			if err := relayconvert.NormalizeKimiK3ChatRequest(converted); err != nil {
				return nil, err
			}
		}
		return converted, nil
	}
	if !info.IsKimiK3OfficialCompatibility() {
		request.ResponseFormat = nil
	}
	return request, nil
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.IsTNTTencentOpenAIConversion() {
		return fmt.Sprintf("%s/v1/chat/completions", strings.TrimRight(info.ChannelBaseUrl, "/")), nil
	}
	requestURL := fmt.Sprintf("%s/v1/messages", info.ChannelBaseUrl)
	if !shouldAppendClaudeBetaQuery(info) {
		return requestURL, nil
	}

	parsedURL, err := url.Parse(requestURL)
	if err != nil {
		return "", err
	}
	query := parsedURL.Query()
	query.Set("beta", "true")
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String(), nil
}

func shouldAppendClaudeBetaQuery(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	if info.IsClaudeBetaQuery {
		return true
	}
	if info.ChannelOtherSettings.ClaudeBetaQuery {
		return true
	}
	return false
}

func CommonClaudeHeadersOperation(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) {
	// common headers operation
	anthropicBeta := c.Request.Header.Get("anthropic-beta")
	if anthropicBeta != "" {
		req.Set("anthropic-beta", anthropicBeta)
	}
	model_setting.GetClaudeSettings().WriteHeaders(info.OriginModelName, req)
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	if info.IsTNTTencentOpenAIConversion() {
		channel.SetupApiRequestHeader(info, c, req)
		req.Set("Authorization", "Bearer "+info.ApiKey)
		return nil
	}
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("x-api-key", info.ApiKey)
	anthropicVersion := c.Request.Header.Get("anthropic-version")
	if anthropicVersion == "" {
		anthropicVersion = "2023-06-01"
	}
	req.Set("anthropic-version", anthropicVersion)
	CommonClaudeHeadersOperation(c, req, info)
	return nil
}

func (a *Adaptor) FinalizeRequestHeader(_ *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	if !info.IsTNTTencentOpenAIConversion() {
		return nil
	}
	for name := range req.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "x-api-key" || lower == "anthropic-version" ||
			lower == "openai-organization" || lower == "openai-project" || lower == "openai-beta" ||
			lower == "x-app" || lower == "host" || lower == "content-length" || lower == "connection" ||
			strings.HasPrefix(lower, "x-stainless-") || strings.HasPrefix(lower, "x-codex-") {
			req.Header.Del(name)
		}
	}
	req.Host = ""
	userAgent := "App/1.0"
	if info.RelayFormat == types.RelayFormatClaude {
		userAgent = "ChatGPT/1.0"
	}
	req.Header.Set("Authorization", "Bearer "+info.ApiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", userAgent)
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if info.IsTNTTencentOpenAIConversion() {
		if err := relayconvert.SanitizeTNTTencentChatRequest(request); err != nil {
			return nil, err
		}
		return request, nil
	}
	result, err := relayconvert.ConvertRequest(c, info, types.RelayFormatClaude, request)
	if err != nil {
		return nil, err
	}
	converted, ok := result.Value.(*dto.ClaudeRequest)
	if !ok {
		return nil, fmt.Errorf("expected Anthropic messages request, got %T", result.Value)
	}
	if info.IsKimiK3OfficialCompatibility() {
		if request.ResponseFormat != nil {
			responseFormat, err := common.Marshal(request.ResponseFormat)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal response_format: %w", err)
			}
			converted.ResponseFormat = responseFormat
		}
		if request.ReasoningEffort != "" {
			outputConfig, err := common.Marshal(dto.OutputConfigForEffort{Effort: request.ReasoningEffort})
			if err != nil {
				return nil, err
			}
			converted.OutputConfig = outputConfig
		}
		if err := relayconvert.NormalizeKimiK3ClaudeRequest(converted); err != nil {
			return nil, err
		}
	}
	return converted, nil
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if info.IsTNTTencentOpenAIConversion() {
		converted, err := relayconvert.ConvertTNTTencentResponsesRequest(&request)
		if err != nil {
			return nil, err
		}
		if info.IsKimiK3OfficialCompatibility() {
			if err := relayconvert.NormalizeKimiK3ChatRequest(converted); err != nil {
				return nil, err
			}
		}
		return converted, nil
	}
	if info.IsKimiK3OfficialCompatibility() {
		converted, err := relayconvert.OpenAIResponsesRequestToClaudeMessages(c, info, &request)
		if err != nil {
			return nil, err
		}
		chatRequest, err := relayconvert.ResponsesRequestToChatCompletionsRequest(&request)
		if err != nil {
			return nil, err
		}
		if chatRequest.ResponseFormat != nil {
			responseFormat, err := common.Marshal(chatRequest.ResponseFormat)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal response_format: %w", err)
			}
			converted.ResponseFormat = responseFormat
		}
		if request.Reasoning != nil && request.Reasoning.Effort != "" {
			outputConfig, err := common.Marshal(dto.OutputConfigForEffort{Effort: request.Reasoning.Effort})
			if err != nil {
				return nil, err
			}
			converted.OutputConfig = outputConfig
		}
		if err := relayconvert.NormalizeKimiK3ClaudeRequest(converted); err != nil {
			return nil, err
		}
		return converted, nil
	}
	return nil, errors.New("not implemented")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if info.IsTNTTencentOpenAIConversion() {
		info.FinalRequestRelayFormat = types.RelayFormatOpenAI
		upstreamStream := resp != nil && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
		if upstreamStream {
			if !info.IsStream {
				return openaiadapter.OaiChatBufferedStreamHandler(c, info, resp)
			}
			if info.RelayMode == relayconstant.RelayModeResponses {
				return openaiadapter.OaiChatToResponsesStreamHandler(c, info, resp)
			}
			return openaiadapter.OaiStreamHandler(c, info, resp)
		}
		if info.RelayMode == relayconstant.RelayModeResponses {
			return openaiadapter.OaiChatToResponsesHandler(c, info, resp)
		}
		return openaiadapter.OpenaiHandler(c, info, resp)
	}
	info.FinalRequestRelayFormat = types.RelayFormatClaude
	if info.IsKimiK3OfficialCompatibility() && info.RelayMode == relayconstant.RelayModeResponses {
		if info.IsStream {
			return ClaudeToResponsesStreamHandler(c, resp, info)
		}
		return ClaudeToResponsesHandler(c, resp, info)
	}
	if info.IsStream {
		return ClaudeStreamHandler(c, resp, info)
	} else {
		return ClaudeHandler(c, resp, info)
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
