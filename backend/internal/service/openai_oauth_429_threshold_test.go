package service

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountGetOpenAIOAuth429ConsecutiveThreshold(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want int64
	}{
		{name: "minimum int", raw: 1, want: 1},
		{name: "int32", raw: int32(9), want: 9},
		{name: "int64", raw: int64(10), want: 10},
		{name: "float32 integer", raw: float32(11), want: 11},
		{name: "float64 integer", raw: float64(12), want: 12},
		{name: "json number", raw: json.Number("13"), want: 13},
		{name: "integer string", raw: " 14 ", want: 14},
		{name: "maximum", raw: 100, want: 100},
		{name: "zero", raw: 0, want: 10},
		{name: "negative", raw: -1, want: 10},
		{name: "above maximum", raw: 101, want: 10},
		{name: "fraction", raw: 1.5, want: 10},
		{name: "json fraction", raw: json.Number("2.5"), want: 10},
		{name: "string fraction", raw: "2.5", want: 10},
		{name: "nan", raw: math.NaN(), want: 10},
		{name: "positive infinity", raw: math.Inf(1), want: 10},
		{name: "non numeric string", raw: "ten", want: 10},
		{name: "boolean", raw: true, want: 10},
		{name: "nil", raw: nil, want: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{Extra: map[string]any{openAIOAuth429ConsecutiveThresholdExtraKey: tt.raw}}
			require.Equal(t, tt.want, account.GetOpenAIOAuth429ConsecutiveThreshold())
		})
	}
}

func TestAccountGetOpenAIOAuth429ConsecutiveThresholdDefaults(t *testing.T) {
	var nilAccount *Account
	require.Equal(t, int64(10), nilAccount.GetOpenAIOAuth429ConsecutiveThreshold())
	require.Equal(t, int64(10), (&Account{}).GetOpenAIOAuth429ConsecutiveThreshold())
	require.Equal(t, int64(10), (&Account{Extra: map[string]any{}}).GetOpenAIOAuth429ConsecutiveThreshold())
}
