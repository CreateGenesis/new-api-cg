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

func TestValidateSimulatedModelCacheMaxEntriesPerScope(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "minimum", value: "1"},
		{name: "default", value: "100"},
		{name: "maximum", value: "5000"},
		{name: "zero", value: "0", wantErr: true},
		{name: "too large", value: "5001", wantErr: true},
		{name: "fraction", value: "100.5", wantErr: true},
		{name: "not a number", value: "invalid", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSimulatedModelCacheMaxEntriesPerScope(test.value)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateSimulatedModelCacheMinInputTokens(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "disabled", value: "0"},
		{name: "default", value: "128"},
		{name: "maximum", value: "1000000"},
		{name: "negative", value: "-1", wantErr: true},
		{name: "too large", value: "1000001", wantErr: true},
		{name: "fraction", value: "512.5", wantErr: true},
		{name: "not a number", value: "invalid", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSimulatedModelCacheMinInputTokens(test.value)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
