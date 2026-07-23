package store

import "testing"

func TestSharesResourceIncludesSameLocationAcrossLanes(t *testing.T) {
	if !sharesResource([]int{2}, []int{10}, []int{1}, []int{10}) {
		t.Fatal("same Location on another Lane was not treated as an affected resource")
	}
	if sharesResource([]int{2}, []int{20}, []int{1}, []int{10}) {
		t.Fatal("unrelated Lane and Location were treated as an affected resource")
	}
}
