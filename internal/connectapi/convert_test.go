package connectapi_test

import (
	"slices"
	"testing"

	"github.com/dotwaffle/beamers/internal/connectapi"
)

func TestInt64sWidensHostIntegers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		values   []int
		expected []int64
	}{
		{name: "empty", values: nil, expected: []int64{}},
		{name: "identifiers", values: []int{1, 7, 42}, expected: []int64{1, 7, 42}},
		{name: "negative", values: []int{-3}, expected: []int64{-3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := connectapi.Int64s(test.values)
			if !slices.Equal(got, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, got)
			}
		})
	}
}

func TestIntsNarrowsProtobufIdentifiers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		values   []int64
		expected []int
	}{
		{name: "empty", values: nil, expected: []int{}},
		{name: "identifiers", values: []int64{1, 7, 42}, expected: []int{1, 7, 42}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := connectapi.Ints(test.values)
			if !slices.Equal(got, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, got)
			}
		})
	}
}

func TestPositiveInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		value       int64
		expected    int
		expectedErr string
	}{
		{name: "positive", value: 12, expected: 12},
		{name: "zero", value: 0, expectedErr: "event_id must be a positive supported integer"},
		{name: "negative", value: -1, expectedErr: "event_id must be a positive supported integer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := connectapi.PositiveInt("event_id", test.value)
			if test.expectedErr != "" {
				if err == nil || err.Error() != test.expectedErr {
					t.Fatalf("expected %q, got %v", test.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.expected {
				t.Fatalf("expected %d, got %d", test.expected, got)
			}
		})
	}
}

func TestPositiveInts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		values      []int64
		expected    []int
		expectedErr string
	}{
		{name: "empty", values: nil, expected: []int{}},
		{name: "identifiers", values: []int64{3, 9}, expected: []int{3, 9}},
		{
			name:        "rejects the first unsupported value",
			values:      []int64{3, 0, 9},
			expectedErr: "lane_ids must be a positive supported integer",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := connectapi.PositiveInts("lane_ids", test.values)
			if test.expectedErr != "" {
				if err == nil || err.Error() != test.expectedErr {
					t.Fatalf("expected %q, got %v", test.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !slices.Equal(got, test.expected) {
				t.Fatalf("expected %v, got %v", test.expected, got)
			}
		})
	}
}

func TestNonNegativeInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		value       int64
		expected    int
		expectedErr string
	}{
		{name: "zero", value: 0, expected: 0},
		{name: "positive", value: 5, expected: 5},
		{name: "negative", value: -1, expectedErr: "offset must be a nonnegative supported integer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := connectapi.NonNegativeInt("offset", test.value)
			if test.expectedErr != "" {
				if err == nil || err.Error() != test.expectedErr {
					t.Fatalf("expected %q, got %v", test.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.expected {
				t.Fatalf("expected %d, got %d", test.expected, got)
			}
		})
	}
}
