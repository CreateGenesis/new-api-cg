/*
Copyright (C) 2023-2026 QuantumNous
*/

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestClassifyMoonshotUsagePayloadUsesExhaustedFiveHourWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	classified := classifyMoonshotUsagePayload(map[string]any{
		"usage": map[string]any{"limit": float64(1000), "used": float64(400)},
		"limits": []any{
			map[string]any{
				"detail": map[string]any{"name": "5h", "limit": float64(100), "used": float64(100), "reset_in": float64(1200)},
			},
		},
	}, now)

	require.NotNil(t, classified)
	require.Equal(t, MoonshotQuotaWindowFiveHour, classified.Window)
	require.Equal(t, now.Unix()+1200, classified.Until)
	require.Equal(t, now.Unix()+1200, classified.FiveHourUntil)
	require.Zero(t, classified.WeeklyUntil)
}

func TestClassifyMoonshotUsagePayloadUsesWindowDurationWhenDetailHasNoName(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	classified := classifyMoonshotUsagePayload(map[string]any{
		"limits": []any{
			map[string]any{
				"detail": map[string]any{"limit": float64(100), "remaining": float64(0), "reset_in": float64(600)},
				"window": map[string]any{"duration": float64(5), "timeUnit": "HOUR"},
			},
		},
	}, now)

	require.NotNil(t, classified)
	require.Equal(t, MoonshotQuotaWindowFiveHour, classified.Window)
	require.Equal(t, now.Unix()+600, classified.Until)
}

func TestClassifyMoonshotUsagePayloadUsesKimiCodeUsageShape(t *testing.T) {
	now := time.Date(2026, 8, 22, 5, 0, 0, 0, time.UTC)
	classified := classifyMoonshotUsagePayload(map[string]any{
		"usage": map[string]any{
			"limit":     "100",
			"used":      "45",
			"remaining": "55",
			"resetTime": "2026-08-25T12:47:03.169622Z",
		},
		"limits": []any{
			map[string]any{
				"window": map[string]any{
					"duration": float64(300),
					"timeUnit": "TIME_UNIT_MINUTE",
				},
				"detail": map[string]any{
					"limit":     "100",
					"used":      "100",
					"resetTime": "2026-08-22T05:47:03.169622Z",
				},
			},
		},
	}, now)

	require.NotNil(t, classified)
	require.Equal(t, MoonshotQuotaWindowFiveHour, classified.Window)
	require.Equal(t, int64(1787377623), classified.Until)
	require.Equal(t, int64(1787377623), classified.FiveHourUntil)
	require.Zero(t, classified.WeeklyUntil)
}

func TestClassifyMoonshotUsagePayloadTreatsTopLevelUsageAsWeekly(t *testing.T) {
	now := time.Date(2026, 8, 22, 5, 0, 0, 0, time.UTC)
	classified := classifyMoonshotUsagePayload(map[string]any{
		"usage": map[string]any{
			"limit":     "100",
			"used":      "100",
			"remaining": "0",
			"resetTime": "2026-08-25T12:47:03.169622Z",
		},
	}, now)

	require.NotNil(t, classified)
	require.Equal(t, MoonshotQuotaWindowWeekly, classified.Window)
	require.Equal(t, int64(1787662023), classified.Until)
	require.Equal(t, int64(1787662023), classified.WeeklyUntil)
	require.Zero(t, classified.FiveHourUntil)
}

func TestClassifyMoonshotUsagePayloadUsesExhaustedWeeklyWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	classified := classifyMoonshotUsagePayload(map[string]any{
		"data": []any{
			map[string]any{
				"model_name": "all",
				"limit":      float64(100),
				"used":       float64(100),
				"resetTime":  float64(1_700_007_200),
			},
		},
	}, now)

	require.NotNil(t, classified)
	require.Equal(t, MoonshotQuotaWindowWeekly, classified.Window)
	require.Equal(t, int64(1_700_007_200), classified.Until)
	require.Equal(t, int64(1_700_007_200), classified.WeeklyUntil)
	require.Zero(t, classified.FiveHourUntil)
}

func TestClassifyMoonshotUsagePayloadIgnoresNonExhaustedWindows(t *testing.T) {
	classified := classifyMoonshotUsagePayload(map[string]any{
		"limits": []any{
			map[string]any{
				"detail": map[string]any{"name": "5h", "limit": float64(100), "used": float64(99)},
			},
		},
	}, time.Unix(1_700_000_000, 0))
	require.Nil(t, classified)
}

func TestMoonshotUsageLookupErrorIsDeferredToUsageAPI(t *testing.T) {
	err := types.NewErrorWithStatusCode(errors.New("upstream error"), types.ErrorCodeDoRequestFailed, http.StatusForbidden)
	err.SetUpstreamResponse(http.StatusForbidden, `{"error":{"message":"You've reached your usage limit for this billing cycle. Your quota will be refreshed in the next cycle. To continue now, purchase extra usage or upgrade your plan: https://www.kimi.com/code/#pricing","type":"access_terminated_error"}}`)

	require.True(t, isMoonshotUsageLookupError(err))
	require.Nil(t, ClassifyMoonshotQuotaError(err, time.Now()))
}

func TestFetchMoonshotUsageSendsKeyAndUsesUsagesEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/usages", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.Equal(t, "KimiCLI/1.6", r.Header.Get("User-Agent"))
		require.Equal(t, "kimi_cli", r.Header.Get("X-Msh-Platform"))
		require.NotEmpty(t, r.Header.Get("X-Msh-Version"))
		require.NotEmpty(t, r.Header.Get("X-Msh-Device-Name"))
		require.NotEmpty(t, r.Header.Get("X-Msh-Device-Model"))
		require.Equal(t, runtime.GOOS+" "+runtime.GOARCH, r.Header.Get("X-Msh-Device-Model"))
		require.NotEmpty(t, r.Header.Get("X-Msh-Os-Version"))
		require.Equal(t, runtime.GOOS, r.Header.Get("X-Msh-Os-Version"))
		require.NotEmpty(t, r.Header.Get("X-Msh-Device-Id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"model_name":"all","limit":100,"used":100}]}`))
	}))
	defer server.Close()

	payload, err := fetchMoonshotUsage(context.Background(), server.Client(), server.URL+"/v1", "test-key")
	require.NoError(t, err)
	require.Equal(t, "all", payload["data"].([]any)[0].(map[string]any)["model_name"])
}
