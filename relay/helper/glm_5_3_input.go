package helper

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

const glm53CompatibilityDecodeErrorKey = "glm_5_3_compatibility_decode_error"

func unmarshalBodyWithGLM53Fallback(c *gin.Context, target any, format types.RelayFormat) error {
	decodeErr := common.UnmarshalBodyReusable(c, target)
	if decodeErr == nil {
		return nil
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return decodeErr
	}
	body, err := storage.Bytes()
	if err != nil {
		return decodeErr
	}
	normalized, err := relayconvert.NormalizeGLM53RequestJSON(body, format)
	if err != nil {
		return decodeErr
	}
	switch typed := target.(type) {
	case *dto.GeneralOpenAIRequest:
		*typed = dto.GeneralOpenAIRequest{}
	case *dto.ClaudeRequest:
		*typed = dto.ClaudeRequest{}
	default:
		return decodeErr
	}
	if err := common.Unmarshal(normalized, target); err != nil {
		return decodeErr
	}
	c.Set(glm53CompatibilityDecodeErrorKey, decodeErr.Error())
	return nil
}

func GLM53CompatibilityDecodeError(c *gin.Context) error {
	message := c.GetString(glm53CompatibilityDecodeErrorKey)
	if message == "" {
		return nil
	}
	return errors.New(message)
}

func NormalizeGLM53RequestBody(c *gin.Context, format types.RelayFormat, target any) error {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return err
	}
	body, err := storage.Bytes()
	if err != nil {
		return err
	}
	normalized, err := relayconvert.NormalizeGLM53RequestJSON(body, format)
	if err != nil {
		return err
	}
	return common.Unmarshal(normalized, target)
}
