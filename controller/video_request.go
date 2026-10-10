package controller

import (
	"errors"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/videocompat"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const videoRequestContextKey = "video_request_variants"

type videoRequestVariant struct {
	body      common.BodyStorage
	estimates *dto.InputTokenEstimates
	tokens    int
	meta      *types.TokenCountMeta
	converted bool
}

type videoRequestVariants struct {
	original        common.BodyStorage
	protocol        string
	format          types.RelayFormat
	session         *videocompat.Session
	variants        map[string]*videoRequestVariant
	billingPrepared bool
}

func newVideoRequestVariants(c *gin.Context, format types.RelayFormat) (*videoRequestVariants, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	protocol := "chat"
	switch format {
	case types.RelayFormatClaude:
		protocol = "messages"
	case types.RelayFormatOpenAIResponses:
		protocol = "responses"
	}
	v := &videoRequestVariants{original: storage, protocol: protocol, format: format, variants: make(map[string]*videoRequestVariant)}
	c.Set(videoRequestContextKey, v)
	return v, nil
}

func (v *videoRequestVariants) Close() {
	for _, variant := range v.variants {
		if variant.body != v.original {
			variant.body.Close()
		}
	}
	if v.session != nil {
		v.session.Close()
	}
	v.original.Close()
}

func bindVideoBody(c *gin.Context, storage common.BodyStorage, converted bool) error {
	if _, err := storage.Seek(0, io.SeekStart); err != nil {
		return err
	}
	c.Set(common.KeyBodyStorage, storage)
	c.Set(common.KeyRequestBody, nil)
	c.Set("video_effective_images", converted)
	c.Request.Body = io.NopCloser(storage)
	c.Request.ContentLength = storage.Size()
	c.Request.GetBody = storage.NewReader
	return nil
}

func (v *videoRequestVariants) prepare(c *gin.Context, info *relaycommon.RelayInfo, channel *model.Channel) (*videoRequestVariant, error) {
	settings := channel.GetOtherSettings().VideoCompatibility
	key := "native"
	converted := settings != nil && settings.Enabled
	if converted {
		encoded, err := common.Marshal(settings)
		if err != nil {
			return nil, err
		}
		key = string(encoded)
	}
	if cached := v.variants[key]; cached != nil {
		return cached, nil
	}
	storage := v.original
	if converted {
		if v.session == nil {
			var err error
			v.session, err = videocompat.NewStorageSession(c.Request.Context(), v.original, v.protocol)
			if err != nil {
				return nil, err
			}
		}
		var err error
		storage, err = v.session.VariantStorage(*settings)
		if err != nil {
			return nil, err
		}
	}
	if err := bindVideoBody(c, storage, converted); err != nil {
		if converted {
			storage.Close()
		}
		return nil, err
	}
	request, err := helper.GetAndValidateRequest(c, v.format)
	if err != nil {
		if converted {
			storage.Close()
		}
		return nil, err
	}
	meta := request.GetTokenCountMeta()
	probe := *info
	probe.Request = request
	tokens, err := service.CountRequestToken(c, meta, &probe)
	if err != nil {
		if converted {
			storage.Close()
		}
		return nil, err
	}
	variant := &videoRequestVariant{body: storage, estimates: service.InputTokenEstimatesForRouting(v.format, tokens, meta, info.UpstreamAttemptModelName()), tokens: tokens, meta: meta, converted: converted}
	v.variants[key] = variant
	return variant, nil
}

// bind creates a fresh DTO even for same-channel retries: adaptors may mutate it.
func (v *videoRequestVariants) bind(c *gin.Context, info *relaycommon.RelayInfo, channel *model.Channel, param *service.ChannelSelectParam) *types.NewAPIError {
	variant, err := v.prepare(c, info, channel)
	if err != nil {
		return videoRequestError(err)
	}
	if err := bindVideoBody(c, variant.body, variant.converted); err != nil {
		return videoRequestError(err)
	}
	request, err := helper.GetAndValidateRequest(c, v.format)
	if err != nil {
		return videoRequestError(err)
	}
	info.Request = request
	info.EffectiveTokenCountMeta = variant.meta
	tokens := variant.tokens
	if !constant.CountToken {
		tokens = 0
	}
	info.SetEstimatePromptTokens(tokens)
	common.SetContextKey(c, constant.ContextKeyPromptTokens, tokens)
	param.InputTokenEstimates = variant.estimates
	return nil
}

func bindSelectedVideoVariant(c *gin.Context, info *relaycommon.RelayInfo, channel *model.Channel, param *service.ChannelSelectParam) *types.NewAPIError {
	value, ok := c.Get(videoRequestContextKey)
	if !ok {
		return nil
	}
	return value.(*videoRequestVariants).bind(c, info, channel, param)
}

func prepareSelectedVideoBilling(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	value, ok := c.Get(videoRequestContextKey)
	if !ok {
		return nil
	}
	v := value.(*videoRequestVariants)
	if !v.billingPrepared {
		if err := relay.PrepareRequestBilling(c, info); err != nil {
			return err
		}
		v.billingPrepared = true
		return nil
	}
	return relay.RefreshVariantBilling(c, info, info.GetEstimatePromptTokens())
}

func videoRequestError(err error) *types.NewAPIError {
	status := http.StatusServiceUnavailable
	// All media failures are local and never enter upstream retry/auto-ban logic.
	var mediaErr *videocompat.Error
	if errors.As(err, &mediaErr) {
		status = mediaErr.StatusCode
	}
	return types.NewErrorWithStatusCode(err, "video_processing_failed", status, types.ErrOptionWithSkipRetry())
}
