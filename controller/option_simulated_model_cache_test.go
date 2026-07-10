package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSimulatedModelCacheMemoryBudgetMB(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "minimum", value: "1"},
		{name: "default", value: "1024"},
		{name: "maximum", value: "1048576"},
		{name: "zero", value: "0", wantErr: true},
		{name: "too large", value: "1048577", wantErr: true},
		{name: "fraction", value: "512.5", wantErr: true},
		{name: "not a number", value: "invalid", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSimulatedModelCacheMemoryBudgetMB(test.value)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
