/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/

package service

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type MoonshotQuotaWindow string

const (
	MoonshotQuotaWindowFiveHour              MoonshotQuotaWindow = "five_hour"
	MoonshotQuotaWindowWeekly                MoonshotQuotaWindow = "weekly"
	MoonshotQuotaWindowMonthlyNoSubscription MoonshotQuotaWindow = "monthly_no_subscription"
)

const (
	moonshotFiveHourReason = "Moonshot 5-hour quota exhausted"
	moonshotWeeklyReason   = "Moonshot weekly quota exhausted"
	moonshotMonthlyReason  = "Moonshot monthly quota requires a subscription"
)

type MoonshotQuotaClassification struct {
	Window MoonshotQuotaWindow
	Until  int64
	Reason string
}

var (
	moonshotFiveHourPattern          = regexp.MustCompile(`(?i)(5\s*[- ]?hour[\s_-]*(quota|limit|window)|5\s*h[\s_-]*(quota|limit|window)|five\s+hour[\s_-]*(quota|limit|window)|五\s*小时(?:额度|限额|窗口))`)
	moonshotWeeklyPattern            = regexp.MustCompile(`(?i)(\bweekly[\s_-]*(quota|limit|window)\b|\bweek[\s_-]*(quota|limit|window)\b|周额度|每周|周限额)`)
	moonshotMonthlyPattern           = regexp.MustCompile(`(?i)(monthly|month|月额度|每月|月)`)
	moonshotSubscriptionPattern      = regexp.MustCompile(`(?i)(subscription|subscribe|subscribing|订阅|套餐)`)
	moonshotNeedsSubscriptionPattern = regexp.MustCompile(`(?i)(not\s+subscribed|no\s+subscription|without\s+subscription|subscription\s+required|requires?\s+subscription|active\s+subscription\s+required|需要订阅|未订阅|没有订阅|需订阅)`)
)

// ClassifyMoonshotQuotaError only uses the response returned by Moonshot.
// Generic authentication, balance, model, and rate-limit errors therefore do
// not enter the quota automation path.
func ClassifyMoonshotQuotaError(err *types.NewAPIError, now time.Time) *MoonshotQuotaClassification {
	if err == nil || err.GetUpstreamStatusCode() < http.StatusBadRequest || err.GetUpstreamStatusCode() >= http.StatusInternalServerError {
		return nil
	}
	response := strings.TrimSpace(err.GetUpstreamResponse())
	if response == "" {
		return nil
	}
	resetAt, resetIn := moonshotResetValues(response)
	if moonshotMonthlyPattern.MatchString(response) && moonshotSubscriptionPattern.MatchString(response) && moonshotNeedsSubscriptionPattern.MatchString(response) {
		return &MoonshotQuotaClassification{Window: MoonshotQuotaWindowMonthlyNoSubscription, Reason: moonshotMonthlyReason}
	}
	if moonshotWeeklyPattern.MatchString(response) {
		until := moonshotQuotaUntil(resetAt, resetIn, now, 7*24*time.Hour)
		return &MoonshotQuotaClassification{Window: MoonshotQuotaWindowWeekly, Until: until, Reason: moonshotWeeklyReason}
	}
	if moonshotFiveHourPattern.MatchString(response) {
		until := moonshotQuotaUntil(resetAt, resetIn, now, 5*time.Hour)
		return &MoonshotQuotaClassification{Window: MoonshotQuotaWindowFiveHour, Until: until, Reason: moonshotFiveHourReason}
	}
	return nil
}

func moonshotQuotaUntil(resetAt, resetIn int64, now time.Time, fallback time.Duration) int64 {
	if resetAt > now.Unix() {
		return resetAt
	}
	if resetIn > 0 && resetIn <= int64((31*24*time.Hour)/time.Second) {
		return now.Add(time.Duration(resetIn) * time.Second).Unix()
	}
	return now.Add(fallback).Unix()
}

func moonshotResetValues(response string) (int64, int64) {
	var payload map[string]any
	if common.Unmarshal([]byte(response), &payload) != nil {
		return 0, 0
	}
	var resetAt, resetIn int64
	var visit func(map[string]any)
	visit = func(values map[string]any) {
		for key, value := range values {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			switch normalizedKey {
			case "resettime", "resetime", "reset_at", "reset_time":
				if parsed := moonshotTimestamp(value); parsed > resetAt {
					resetAt = parsed
				}
			case "resetin", "reset_in":
				if parsed := moonshotInteger(value); parsed > resetIn {
					resetIn = parsed
				}
			}
			if nested, ok := value.(map[string]any); ok {
				visit(nested)
			}
		}
	}
	visit(payload)
	return resetAt, resetIn
}

func moonshotTimestamp(value any) int64 {
	switch parsed := value.(type) {
	case float64:
		return normalizeMoonshotTimestamp(int64(parsed))
	case int64:
		return normalizeMoonshotTimestamp(parsed)
	case string:
		if integer, err := strconv.ParseInt(strings.TrimSpace(parsed), 10, 64); err == nil {
			return normalizeMoonshotTimestamp(integer)
		}
		if timestamp, err := time.Parse(time.RFC3339, strings.TrimSpace(parsed)); err == nil {
			return timestamp.Unix()
		}
	}
	return 0
}

func normalizeMoonshotTimestamp(value int64) int64 {
	if value > 1_000_000_000_000 {
		return value / 1000
	}
	return value
}

func moonshotInteger(value any) int64 {
	switch parsed := value.(type) {
	case float64:
		return int64(parsed)
	case int64:
		return parsed
	case string:
		integer, _ := strconv.ParseInt(strings.TrimSpace(parsed), 10, 64)
		return integer
	}
	return 0
}

// HandleMoonshotQuotaError returns true when the error was recognized as a
// Moonshot quota/subscription response. A recognized monthly response is
// intentionally swallowed when its optional switch is off, preventing the
// generic channel auto-ban from turning this risky detector into an implicit
// mandatory feature.
func HandleMoonshotQuotaError(channelError types.ChannelError, err *types.NewAPIError) bool {
	if channelError.ChannelType != constant.ChannelTypeMoonshot || !channelError.IsMultiKey || !channelError.AutoBan {
		return false
	}
	classification := ClassifyMoonshotQuotaError(err, time.Now())
	if classification == nil {
		return false
	}
	channel, loadErr := model.GetChannelById(channelError.ChannelId, true)
	if loadErr != nil {
		return false
	}
	settings := channel.GetOtherSettings().MoonshotQuotaAutoDisable
	if settings == nil {
		return false
	}
	if classification.Window == MoonshotQuotaWindowMonthlyNoSubscription {
		if !settings.MonthlyNoSubscriptionEnabled {
			return true
		}
	} else if !settings.Enabled {
		return false
	}
	keys := channel.GetKeys()
	keyIndex := -1
	for index, key := range keys {
		if key == channelError.UsingKey {
			keyIndex = index
			break
		}
	}
	if keyIndex < 0 {
		return false
	}
	quotaStatus := model.MoonshotQuotaStatus{}
	if existing := channel.ChannelInfo.MultiKeyMoonshotQuotaStatus[keyIndex]; existing != (model.MoonshotQuotaStatus{}) {
		quotaStatus = existing
	}
	switch classification.Window {
	case MoonshotQuotaWindowFiveHour:
		quotaStatus.FiveHourUntil = classification.Until
	case MoonshotQuotaWindowWeekly:
		quotaStatus.WeeklyUntil = classification.Until
	case MoonshotQuotaWindowMonthlyNoSubscription:
		quotaStatus.MonthlyNoSubscription = true
	}
	if err := model.UpdateMoonshotQuotaKey(channel.Id, keyIndex, classification.Reason, quotaStatus); err != nil {
		common.SysLog("failed to update Moonshot quota key status: " + err.Error())
		return false
	}
	return true
}

func RecoverMoonshotQuotaKeys() {
	if _, err := model.RecoverMoonshotQuotaKeys(common.GetTimestamp()); err != nil {
		common.SysLog("failed to recover Moonshot quota keys: " + err.Error())
	}
}
