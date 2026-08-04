package connectapi

import (
	"errors"
	"math"
)

// Int64s widens host identifiers for a protobuf response. The name states the
// element type it produces, because adapters previously spelled both
// directions of this conversion "ints" and disagreed about which way it went.
func Int64s(values []int) []int64 {
	result := make([]int64, 0, len(values))
	for _, value := range values {
		result = append(result, int64(value))
	}
	return result
}

// Ints narrows protobuf identifiers that a validator has already accepted.
func Ints(values []int64) []int {
	result := make([]int, 0, len(values))
	for _, value := range values {
		result = append(result, int(value))
	}
	return result
}

// PositiveInt narrows one protobuf identifier, rejecting values a host integer
// cannot carry and values no record can have.
func PositiveInt(field string, value int64) (int, error) {
	if value <= 0 || value > math.MaxInt {
		return 0, errors.New(field + " must be a positive supported integer")
	}
	return int(value), nil
}

// PositiveID checks one protobuf identifier for validators that do not need
// the narrowed value.
func PositiveID(field string, value int64) error {
	_, err := PositiveInt(field, value)
	return err
}

// PositiveInts narrows protobuf identifiers, rejecting the whole slice as soon
// as one value is unusable.
func PositiveInts(field string, values []int64) ([]int, error) {
	result := make([]int, 0, len(values))
	for _, value := range values {
		converted, err := PositiveInt(field, value)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

// NonNegativeRevision checks one protobuf revision, count, or offset for
// validators that do not need the narrowed value.
func NonNegativeRevision(field string, value int64) error {
	_, err := NonNegativeInt(field, value)
	return err
}

// FirstInvalid returns the first failed check, so one rejection names one
// field instead of concatenating every complaint about the request.
func FirstInvalid(checks ...error) error {
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// NonNegativeInt narrows one protobuf count or offset, which may be zero.
func NonNegativeInt(field string, value int64) (int, error) {
	if value < 0 || value > math.MaxInt {
		return 0, errors.New(field + " must be a nonnegative supported integer")
	}
	return int(value), nil
}
