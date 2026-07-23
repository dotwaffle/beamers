package displays

import "testing"

func FuzzEnrollmentCodeParsing(f *testing.F) {
	f.Add("ABCD-EFGH")
	f.Add(" abcd efgh ")
	f.Fuzz(func(t *testing.T, input string) {
		normalized := normalizeEnrollmentCode(input)
		if validEnrollmentCode(normalized) && normalizeEnrollmentCode(normalized) != normalized {
			t.Fatalf("valid code %q is not normalized", normalized)
		}
	})
}

func FuzzDisplayTokenParsing(f *testing.F) {
	f.Add("")
	f.Add("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	f.Fuzz(func(_ *testing.T, input string) {
		_ = validDisplayToken(input)
	})
}
