package cartographer

import (
	"testing"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "bare JSON",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name: "```json fences",
			input: "```json\n{\"key\": \"value\"}\n```",
			expected: "\n{\"key\": \"value\"}\n",
		},
		{
			name: "plain ``` fences",
			input: "```\n{\"key\": \"value\"}\n```",
			expected: "\n{\"key\": \"value\"}\n",
		},
		{
			name: "nested fences",
			input: "```json\n```\n{\"key\": \"value\"}\n```\n```",
			expected: "\n",
		},
		{
			name: "missing closing fence",
			input: "```json\n{\"key\": \"value\"}",
			expected: "\n{\"key\": \"value\"}",
		},
		{
			name: "missing closing plain fence",
			input: "```\n{\"key\": \"value\"}",
			expected: "\n{\"key\": \"value\"}",
		},
		{
			name: "empty string",
			input: "",
			expected: "",
		},
		{
			name: "no fences but contains ```",
			input: "this is a ``` test",
			expected: " test", // Current logic chops off start of string up to ```
		},
		{
			name: "no fences but contains ```json",
			input: "this is a ```json test",
			expected: " test", // Current logic chops off start of string up to ```json
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.expected {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIndexOf(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected int
	}{
		{
			name:     "found at start",
			s:        "hello world",
			substr:   "hello",
			expected: 0,
		},
		{
			name:     "found in middle",
			s:        "hello world",
			substr:   "world",
			expected: 6,
		},
		{
			name:     "not found",
			s:        "hello world",
			substr:   "foo",
			expected: -1,
		},
		{
			name:     "empty substring",
			s:        "hello",
			substr:   "",
			expected: 0,
		},
		{
			name:     "empty string",
			s:        "",
			substr:   "foo",
			expected: -1,
		},
		{
			name:     "both empty",
			s:        "",
			substr:   "",
			expected: 0,
		},
		{
			name:     "substring longer than string",
			s:        "foo",
			substr:   "foobar",
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indexOf(tt.s, tt.substr)
			if got != tt.expected {
				t.Errorf("indexOf(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.expected)
			}
		})
	}
}
