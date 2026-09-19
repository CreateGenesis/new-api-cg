package dto

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// GroupSchedulingSettings affects routing only, never customer billing.
type GroupSchedulingSettings struct {
	Enabled         bool     `json:"enabled"`
	Groups          []string `json:"groups"`
	CostFactor      *float64 `json:"cost_factor,omitempty"`
	ProbeModels     []string `json:"probe_models"`
	IntervalSeconds int      `json:"interval_seconds"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
}

func (s *GroupSchedulingSettings) Interval() int {
	if s == nil || s.IntervalSeconds == 0 {
		return 30
	}
	return s.IntervalSeconds
}
func (s *GroupSchedulingSettings) Timeout() int {
	if s == nil || s.TimeoutSeconds == 0 {
		return 30
	}
	return s.TimeoutSeconds
}
func (s *GroupSchedulingSettings) Cost() float64 {
	if s == nil || s.CostFactor == nil {
		return 1
	}
	return *s.CostFactor
}
func (s *GroupSchedulingSettings) Includes(group, model string) bool {
	return s != nil && s.Enabled && slices.Contains(s.Groups, group) && slices.Contains(s.ProbeModels, model)
}
func (s *GroupSchedulingSettings) Validate(groups, models []string) error {
	if s == nil {
		return nil
	}
	if math.IsNaN(s.Cost()) || math.IsInf(s.Cost(), 0) || s.Cost() < 0 {
		return fmt.Errorf("group_scheduling.cost_factor must be finite and non-negative")
	}
	if s.Interval() < 1 || s.Interval() > 86400 || s.Timeout() < 1 || s.Timeout() > 600 {
		return fmt.Errorf("group_scheduling: interval must be 1-86400 seconds and timeout 1-600 seconds")
	}
	if !s.Enabled {
		return nil
	}
	if len(s.Groups) == 0 || len(s.ProbeModels) == 0 {
		return fmt.Errorf("group_scheduling requires groups and probe models")
	}
	for _, group := range s.Groups {
		if strings.TrimSpace(group) == "" || !slices.Contains(groups, group) {
			return fmt.Errorf("group_scheduling: group %q is not assigned to this channel", group)
		}
	}
	for _, model := range s.ProbeModels {
		if strings.TrimSpace(model) == "" || !slices.Contains(models, model) {
			return fmt.Errorf("group_scheduling: model %q is not supported by this channel", model)
		}
	}
	return nil
}
