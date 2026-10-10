package model

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/videocompat"
)

const VideoProcessingConfigOptionKey = "video_processing_setting.config"

var videoProcessingOptionMutex sync.Mutex

func isVideoProcessingOption(key string) bool {
	if strings.HasPrefix(key, "video_processing_setting.") {
		return true
	}
	switch key {
	case "performance_setting.monitor_enabled", "performance_setting.monitor_cpu_threshold", "performance_setting.monitor_memory_threshold", "performance_setting.monitor_disk_threshold":
		return true
	}
	return false
}

// Video processing has one complete document: accepting partial or unknown
// fields would silently turn omitted limits into zero-valued resource budgets.
func parseVideoProcessingConfig(value string) (videocompat.Config, string, error) {
	var config videocompat.Config
	if len(value) > 64*1024 {
		return config, "", common.NewMessage("Video processing configuration exceeds 64 KiB")
	}
	if err := validateCompleteVideoConfigJSON([]byte(value), reflect.TypeFor[videocompat.Config](), VideoProcessingConfigOptionKey); err != nil {
		return config, "", err
	}
	if err := common.UnmarshalJsonStr(value, &config); err != nil {
		return config, "", common.NewMessage("Invalid video processing configuration: {{error}}", map[string]any{"error": err.Error()})
	}
	if err := config.Validate(); err != nil {
		return config, "", common.NewMessage("Invalid video processing configuration: {{error}}", map[string]any{"error": err.Error()})
	}
	encoded, err := common.Marshal(config)
	if err != nil {
		return config, "", err
	}
	return config, string(encoded), nil
}

func validateCompleteVideoConfigJSON(data []byte, configType reflect.Type, path string) error {
	var fields map[string]common.RawMessage
	if err := common.Unmarshal(data, &fields); err != nil || fields == nil {
		return common.NewMessage("Video processing configuration must be a complete JSON object: {{field}}", map[string]any{"field": path})
	}
	known := make(map[string]reflect.Type, configType.NumField())
	for i := range configType.NumField() {
		field := configType.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		known[name] = field.Type
	}
	for name := range fields {
		if _, exists := known[name]; !exists {
			return common.NewMessage("Unknown video processing field: {{field}}", map[string]any{"field": path + "." + name})
		}
	}
	for name, fieldType := range known {
		value, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return common.NewMessage("Video processing field is required: {{field}}", map[string]any{"field": path + "." + name})
		}
		if fieldType.Kind() == reflect.Struct {
			if err := validateCompleteVideoConfigJSON(value, fieldType, path+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

// Evaluate related writes together so a bulk update can change soft and hard
// thresholds without validating against an intermediate configuration.
func prepareVideoProcessingOptions(values map[string]string) (videocompat.Config, string, error) {
	config := videocompat.GetConfig()
	canonical := ""
	monitor := common.GetPerformanceMonitorConfig()
	for key, value := range values {
		if key == VideoProcessingConfigOptionKey {
			var err error
			config, canonical, err = parseVideoProcessingConfig(value)
			if err != nil {
				return config, "", err
			}
			continue
		}
		if strings.HasPrefix(key, "video_processing_setting.") {
			return config, "", common.NewMessage("Video processing settings require the complete {{option}} option", map[string]any{"option": VideoProcessingConfigOptionKey})
		}
		if !isVideoProcessingOption(key) {
			continue
		}
		if key == "performance_setting.monitor_enabled" {
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return config, "", common.NewMessage("Performance monitor enabled must be a boolean")
			}
			monitor.Enabled = enabled
			continue
		}
		threshold, err := strconv.Atoi(value)
		if err != nil || threshold < 0 || threshold > 100 {
			return config, "", common.NewMessage("Performance monitor threshold must be an integer between 0 and 100: {{field}}", map[string]any{"field": key})
		}
		switch key {
		case "performance_setting.monitor_cpu_threshold":
			monitor.CPUThreshold = threshold
		case "performance_setting.monitor_memory_threshold":
			monitor.MemoryThreshold = threshold
		case "performance_setting.monitor_disk_threshold":
			monitor.DiskThreshold = threshold
		}
	}
	if err := validateVideoProcessingMonitorThresholds(config, monitor); err != nil {
		return config, "", err
	}
	return config, canonical, nil
}

func validateVideoProcessingMonitorThresholds(config videocompat.Config, monitor common.PerformanceMonitorConfig) error {
	if !config.AdaptiveEnabled || !monitor.Enabled {
		return nil
	}
	for _, level := range []videocompat.AdaptiveLevel{config.AdaptiveConfig.Light, config.AdaptiveConfig.Heavy} {
		if monitor.CPUThreshold > 0 && level.CPUThreshold >= monitor.CPUThreshold {
			return common.NewMessage("Adaptive CPU thresholds must be lower than the enabled performance monitor CPU threshold")
		}
		if monitor.MemoryThreshold > 0 && level.MemoryThreshold >= monitor.MemoryThreshold {
			return common.NewMessage("Adaptive memory thresholds must be lower than the enabled performance monitor memory threshold")
		}
	}
	return nil
}
