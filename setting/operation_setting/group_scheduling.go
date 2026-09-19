package operation_setting

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const GroupSchedulingToleranceKey = "GroupSchedulingTolerance"

func ValidateGroupSchedulingTolerance(value string) error {
	var thresholds map[string]int64
	if err := common.UnmarshalJsonStr(value, &thresholds); err != nil {
		return err
	}
	if thresholds == nil {
		return fmt.Errorf("group scheduling thresholds must be an object")
	}
	for group, milliseconds := range thresholds {
		if strings.TrimSpace(group) == "" || milliseconds < 0 || milliseconds > 600000 {
			return fmt.Errorf("group scheduling thresholds must be 0-600000 milliseconds with non-empty group names")
		}
	}
	return nil
}

func GroupSchedulingTolerance(group string) int64 {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[GroupSchedulingToleranceKey]
	common.OptionMapRWMutex.RUnlock()
	var thresholds map[string]int64
	if common.UnmarshalJsonStr(value, &thresholds) == nil {
		if threshold, ok := thresholds[group]; ok && threshold >= 0 && threshold <= 600000 {
			return threshold
		}
	}
	return 5000
}
