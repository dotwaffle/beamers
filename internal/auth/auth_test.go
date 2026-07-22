package auth

import "testing"

func TestPasswordWorkAdmissionEnforcesMemoryBudget(t *testing.T) {
	service := &Service{passwordWork: make(chan struct{}, passwordConcurrency)}
	if !service.beginPasswordWork() {
		t.Fatal("first password operation was not admitted")
	}
	if !service.beginPasswordWork() {
		t.Fatal("second password operation was not admitted")
	}
	if service.beginPasswordWork() {
		t.Fatal("third password operation exceeded the 128 MiB KDF memory budget")
	}

	service.endPasswordWork()
	if !service.beginPasswordWork() {
		t.Fatal("released password capacity was not reusable")
	}
}
