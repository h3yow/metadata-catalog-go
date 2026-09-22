package catalog

import (
	"strings"
	"testing"
)

// judge 打印输入与最终判定依据，满足“单测日志打印输入与判定”的要求。
func judge(t *testing.T, input string, got any, err error) {
	t.Helper()
	if err != nil {
		t.Logf("INPUT: %s\n  => JUDGEMENT: rejected, err=%v", input, err)
	} else {
		t.Logf("INPUT: %s\n  => JUDGEMENT: accepted, result=%+v", input, got)
	}
}

func assertReason(t *testing.T, err error, want RejectReason) {
	t.Helper()
	re, ok := AsRejectError(err)
	if !ok {
		t.Fatalf("expected *RejectError with reason %q, got %v", want, err)
	}
	if re.Reason != want {
		t.Fatalf("expected reject reason %q, got %q (%v)", want, re.Reason, err)
	}
}

func assertNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func errContains(err error, sub string) bool {
	return err != nil && strings.Contains(err.Error(), sub)
}
