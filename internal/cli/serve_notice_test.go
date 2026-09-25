package cli

import (
	"fmt"
	"testing"
)

func TestNoticeBuffer_KeepsHeadTailAndEveryWarning(t *testing.T) {
	b := &noticeBuffer{head: 2, tail: 2}
	for i := 1; i <= 10; i++ {
		severity := "NOTICE"
		if i == 5 {
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
