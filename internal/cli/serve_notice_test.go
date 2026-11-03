package cli

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestNoticeBuffer_KeepsHeadTailAndCappedWarnings(t *testing.T) {
	b := &noticeBuffer{head: 2, tail: 2, maxWarnings: 1}
	for i := 1; i <= 10; i++ {
		severity := "NOTICE"
		if i == 5 || i == 6 {
			severity = "WARNING"
		}
		b.add(severity, fmt.Sprintf("line %d", i), "", "")
	}

	f := b.fields()
	var got []string
	for _, n := range f["notices"].([]notice) {
		got = append(got, n.Severity+" "+n.Message)
	}
	want := []string{"NOTICE line 1", "NOTICE line 2", "WARNING line 5", "NOTICE line 9", "NOTICE line 10"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("notices = %v, want %v", got, want)
	}
	if f["noticesTruncated"] != 5 {
		t.Errorf("noticesTruncated = %v, want 5", f["noticesTruncated"])
	}
}

func TestNoticeBuffer_NoTruncationMarkerWhenWithinCap(t *testing.T) {
	b := newNoticeBuffer()
	b.add("NOTICE", "only line", "", "")

	f := b.fields()
	if _, present := f["noticesTruncated"]; present {
		t.Error("noticesTruncated should be absent when nothing was dropped")
	}
	if notices := f["notices"].([]notice); len(notices) != 1 {
		t.Errorf("notices = %v", notices)
	}
}

func TestNoticeBuffer_CapsMiddleWarnings(t *testing.T) {
	b := newNoticeBuffer()
	const n = 20000
	for i := 1; i <= n; i++ {
		b.add("WARNING", fmt.Sprintf("warning %d", i), "", "")
	}

	f := b.fields()
	notices := f["notices"].([]notice)
	if want := b.head + b.maxWarnings + b.tail; len(notices) != want {
		t.Fatalf("kept %d notices, want %d", len(notices), want)
	}
	if f["noticesTruncated"] != n-len(notices) {
		t.Errorf("noticesTruncated = %v, want %d", f["noticesTruncated"], n-len(notices))
	}
	if notices[0].Message != "warning 1" || notices[len(notices)-1].Message != fmt.Sprintf("warning %d", n) {
		t.Errorf("first/last = %q / %q", notices[0].Message, notices[len(notices)-1].Message)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 32<<10 {
		t.Errorf("result is %d bytes, want under 32 KiB", len(raw))
	}
}
