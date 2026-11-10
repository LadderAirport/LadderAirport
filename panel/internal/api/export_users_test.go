package api

import "time"

// SetAdmissionReapplyDelay overrides the node re-push debounce in tests and
// returns a restore func.
func SetAdmissionReapplyDelay(d time.Duration) func() {
	prev := admissionReapplyDelay
	admissionReapplyDelay = d
	return func() { admissionReapplyDelay = prev }
}
