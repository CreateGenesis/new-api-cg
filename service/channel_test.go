package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
)

func TestShouldDisableChannelExcludesStreamRetryError(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = originalEnabled })

	streamErr := types.NewErrorWithStatusCode(
		errors.New("upstream stream ended unexpectedly"),
		types.ErrorCodeChannelStreamError,
		http.StatusBadGateway,
	)
	permanentErr := types.NewError(errors.New("invalid key"), types.ErrorCodeChannelInvalidKey)

	assert.False(t, ShouldDisableChannel(streamErr))
	assert.True(t, ShouldDisableChannel(permanentErr))
}

func TestShouldDisableChannelExcludesResponseHeaderTimeout(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = originalEnabled })

	timeoutErr := types.NewErrorWithStatusCode(
		errors.New("upstream response headers timed out"),
		types.ErrorCodeChannelResponseHeaderTimeout,
		http.StatusGatewayTimeout,
	)

	assert.False(t, ShouldDisableChannel(timeoutErr))
}

func TestShouldDisableChannelExcludesResponseBodyTimeout(t *testing.T) {
	originalEnabled := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = originalEnabled })

	timeoutErr := types.NewErrorWithStatusCode(
		errors.New("upstream response body timed out"),
		types.ErrorCodeChannelResponseBodyTimeout,
		http.StatusGatewayTimeout,
	)

	assert.False(t, ShouldDisableChannel(timeoutErr))
}
