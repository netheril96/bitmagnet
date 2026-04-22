package fts_test

import (
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/database/fts"
	"github.com/stretchr/testify/assert"
)

func TestTokenize_Unicode(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{
			input:    "Hello 世界",
			expected: []string{"hello", "世", "界"},
		},
		{
			input:    "Café",
			expected: []string{"café"},
		},
		{
			input:    "こんにちは",
			expected: []string{"こ", "ん", "に", "ち", "は"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			actual := fts.TokenizeFlat(tc.input)
			assert.Equal(t, tc.expected, actual)
		})
	}
}
