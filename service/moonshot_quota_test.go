/*
Copyright (C) 2023-2026 QuantumNous
*/

package service

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestClassifyMoonshotQuotaError(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name   string
		status int
		body   string
		window MoonshotQuotaWindow
		until  int64
	}{
		{name: "weekly reset in", status: 429, body: `{"error":"weekly quota exhausted","reset_in":3600}`, window: MoonshotQuotaWindowWeekly, until: now.Unix() + 3600},
		{name: "weekly reset timestamp", status: 429, body: `{"error":"weekly quota exhausted","resetTime":1700007200}`, window: MoonshotQuotaWindowWeekly, until: 1700007200},
		{name: "five hour fallback", status: 429, body: `{"message":"5-hour quota exceeded"}`, window: MoonshotQuotaWindowFiveHour, until: now.Unix() + int64((5 * time.Hour).Seconds())},
		{name: "monthly subscription", status: 403, body: `{"message":"monthly quota unavailable: subscription required; not subscribed"}`, window: MoonshotQuotaWindowMonthlyNoSubscription},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := types.NewErrorWithStatusCode(errors.New("upstream error"), types.ErrorCodeDoRequestFailed, tt.status)
			err.SetUpstreamResponse(tt.status, tt.body)
			classified := ClassifyMoonshotQuotaError(err, now)
			require.NotNil(t, classified)
			require.Equal(t, tt.window, classified.Window)
			if tt.until != 0 {
				require.Equal(t, tt.until, classified.Until)
			} else if tt.window == MoonshotQuotaWindowMonthlyNoSubscription {
				require.Zero(t, classified.Until)
			}
		})
	}
}

func TestClassifyMoonshotQuotaErrorRejectsGenericErrors(t *testing.T) {
	err := types.NewErrorWithStatusCode(errors.New("upstream error"), types.ErrorCodeDoRequestFailed, 401)
	err.SetUpstreamResponse(401, `{"message":"invalid api key"}`)
	require.Nil(t, ClassifyMoonshotQuotaError(err, time.Now()))
	serverErr := types.NewErrorWithStatusCode(errors.New("upstream error"), types.ErrorCodeDoRequestFailed, 500)
	serverErr.SetUpstreamResponse(500, `{"message":"weekly quota exhausted"}`)
	require.Nil(t, ClassifyMoonshotQuotaError(serverErr, time.Now()))
	weeklyModelErr := types.NewErrorWithStatusCode(errors.New("upstream error"), types.ErrorCodeDoRequestFailed, 400)
	weeklyModelErr.SetUpstreamResponse(400, `{"message":"weekly model is unavailable"}`)
	require.Nil(t, ClassifyMoonshotQuotaError(weeklyModelErr, time.Now()))
	fiveHourModelErr := types.NewErrorWithStatusCode(errors.New("upstream error"), types.ErrorCodeDoRequestFailed, 400)
	fiveHourModelErr.SetUpstreamResponse(400, `{"message":"5-hour model is unavailable"}`)
	require.Nil(t, ClassifyMoonshotQuotaError(fiveHourModelErr, time.Now()))
}
