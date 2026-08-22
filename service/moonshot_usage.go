/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const moonshotUsageRequestTimeout = 5 * time.Second

// Kimi Code's usage endpoint requires the same device metadata as the Kimi
// CLI. The ID is process-stable so repeated quota checks identify this relay
// as one client without changing the value on every request.
var moonshotUsageDeviceID = common.NewRequestId()

type moonshotUsageCandidate struct {
	window MoonshotQuotaWindow
	until  int64
}

func classifyMoonshotUsageForChannel(ctx context.Context, channel *model.Channel, apiKey string, now time.Time) (*MoonshotQuotaClassification, error) {
	if channel == nil {
		return nil, fmt.Errorf("nil Moonshot channel")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := GetHttpClientWithProxySettings(channel.GetSetting().Proxy, channel.GetSetting())
	if err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, moonshotUsageRequestTimeout)
	defer cancel()
	payload, err := fetchMoonshotUsage(requestContext, client, channel.GetBaseURL(), apiKey)
	if err != nil {
		return nil, err
	}
	return classifyMoonshotUsagePayload(payload, now), nil
}

func fetchMoonshotUsage(ctx context.Context, client *http.Client, baseURL, apiKey string) (map[string]any, error) {
	if client == nil {
		return nil, fmt.Errorf("nil HTTP client")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("empty Moonshot base URL")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("empty Moonshot API key")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/usages", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "KimiCLI/1.6")
	req.Header.Set("X-Msh-Platform", "kimi_cli")
	req.Header.Set("X-Msh-Version", "1.0.0")
	req.Header.Set("X-Msh-Device-Name", "new-api")
	req.Header.Set("X-Msh-Device-Model", runtime.GOOS+" "+runtime.GOARCH)
	req.Header.Set("X-Msh-Os-Version", runtime.GOOS)
	req.Header.Set("X-Msh-Device-Id", moonshotUsageDeviceID)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Moonshot usage API returned status %d: %s", resp.StatusCode, common.LocalLogPreview(string(body)))
	}

	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid Moonshot usage response: %w", err)
	}
	return payload, nil
}

func classifyMoonshotUsagePayload(payload map[string]any, now time.Time) *MoonshotQuotaClassification {
	if len(payload) == 0 {
		return nil
	}
	var candidates []moonshotUsageCandidate
	var visit func(any, string)
	visit = func(value any, hint string) {
		switch typed := value.(type) {
		case map[string]any:
			localHint := moonshotUsageHint(typed, hint)
			if candidate, ok := moonshotUsageCandidateFromMap(typed, localHint, now); ok {
				candidates = append(candidates, candidate)
			}
			for key, nested := range typed {
				switch nested.(type) {
				case map[string]any, []any:
					childHint := localHint + " " + strings.ToLower(key)
					// Kimi Code exposes the weekly window as the top-level
					// `usage` object, while the five-hour window is listed in
					// `limits` with an explicit duration.
					if strings.EqualFold(strings.TrimSpace(key), "usage") {
						childHint += " weekly"
					}
					if nestedMap, ok := nested.(map[string]any); ok {
						childHint += " " + moonshotUsageHint(nestedMap, "")
					}
					visit(nested, childHint)
				}
			}
		case []any:
			for _, nested := range typed {
				visit(nested, hint)
			}
		}
	}
	visit(payload, "")

	var fiveHourUntil, weeklyUntil int64
	for _, candidate := range candidates {
		switch candidate.window {
		case MoonshotQuotaWindowFiveHour:
			if candidate.until > fiveHourUntil {
				fiveHourUntil = candidate.until
			}
		case MoonshotQuotaWindowWeekly:
			if candidate.until > weeklyUntil {
				weeklyUntil = candidate.until
			}
		}
	}
	if fiveHourUntil == 0 && weeklyUntil == 0 {
		return nil
	}

	classification := &MoonshotQuotaClassification{
		FiveHourUntil: fiveHourUntil,
		WeeklyUntil:   weeklyUntil,
	}
	switch {
	case weeklyUntil > 0:
		classification.Window = MoonshotQuotaWindowWeekly
		classification.Until = weeklyUntil
		classification.Reason = moonshotWeeklyReason
	default:
		classification.Window = MoonshotQuotaWindowFiveHour
		classification.Until = fiveHourUntil
		classification.Reason = moonshotFiveHourReason
	}
	return classification
}

func moonshotUsageCandidateFromMap(values map[string]any, hint string, now time.Time) (moonshotUsageCandidate, bool) {
	limit, hasLimit := moonshotUsageNumber(values, "limit", "limit_amount", "total")
	used, hasUsed := moonshotUsageNumber(values, "used", "used_amount", "usage")
	remaining, hasRemaining := moonshotUsageNumber(values, "remaining", "remaining_amount")
	usedPercent, hasUsedPercent := moonshotUsageNumber(values, "used_percent", "used_percentage", "percent")
	if !hasLimit || limit <= 0 || (!hasUsed && !hasRemaining && !hasUsedPercent) {
		return moonshotUsageCandidate{}, false
	}
	exhausted := (hasUsed && used >= limit) || (hasRemaining && remaining <= 0) || (hasUsedPercent && usedPercent >= 100)
	if !exhausted {
		return moonshotUsageCandidate{}, false
	}
	window := moonshotUsageWindowFromHint(hint)
	if window == "" {
		return moonshotUsageCandidate{}, false
	}
	resetAt, resetIn := moonshotResetValuesFromMap(values)
	fallback := 5 * time.Hour
	if window == MoonshotQuotaWindowWeekly {
		fallback = 7 * 24 * time.Hour
	}
	return moonshotUsageCandidate{
		window: window,
		until:  moonshotQuotaUntil(resetAt, resetIn, now, fallback),
	}, true
}

func moonshotUsageNumber(values map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		for actualKey, value := range values {
			if !strings.EqualFold(strings.TrimSpace(actualKey), key) {
				continue
			}
			switch parsed := value.(type) {
			case float64:
				return parsed, true
			case float32:
				return float64(parsed), true
			case int:
				return float64(parsed), true
			case int64:
				return float64(parsed), true
			case string:
				var number float64
				if _, err := fmt.Sscanf(strings.TrimSpace(parsed), "%f", &number); err == nil {
					return number, true
				}
			}
		}
	}
	return 0, false
}

func moonshotUsageHint(values map[string]any, parent string) string {
	parts := []string{parent}
	var collect func(map[string]any)
	collect = func(current map[string]any) {
		for key, value := range current {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			switch normalizedKey {
			case "name", "title", "label", "model_name", "period", "type", "duration", "timeunit", "time_unit", "window", "limit_window":
				parts = append(parts, normalizedKey)
				switch parsed := value.(type) {
				case string, float64, float32, int, int64:
					parts = append(parts, fmt.Sprint(parsed))
				case map[string]any:
					collect(parsed)
				}
			}
		}
	}
	collect(values)
	return strings.ToLower(strings.Join(parts, " "))
}

func moonshotUsageWindowFromHint(hint string) MoonshotQuotaWindow {
	compact := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(hint), "-", " "), "_", " ")
	if strings.Contains(compact, "5h") || strings.Contains(compact, "5 h") || strings.Contains(compact, "5 hour") || strings.Contains(compact, "five hour") || strings.Contains(compact, "5小时") || strings.Contains(compact, "五小时") || strings.Contains(compact, "300 minute") || (strings.Contains(compact, "duration 300") && strings.Contains(compact, "minute")) || (strings.Contains(compact, "duration 5") && strings.Contains(compact, "hour")) {
		return MoonshotQuotaWindowFiveHour
	}
	if strings.Contains(compact, "weekly") || strings.Contains(compact, "week") || strings.Contains(compact, "7 day") || strings.Contains(compact, "model name all") || (strings.Contains(compact, "duration 7") && strings.Contains(compact, "day")) || strings.Contains(compact, "周") {
		return MoonshotQuotaWindowWeekly
	}
	return ""
}
