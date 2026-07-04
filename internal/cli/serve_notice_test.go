package cli

import (
	"fmt"
	"testing"
)

func TestNoticeBuffer_TailAndTruncation(t *testing.T) {
	b := &noticeBuffer{max: 3}
	for i := 1; i <= 5; i++ {
		b.add(fmt.Sprintf("line %d", i), "", "")
	}

	f := b.fields()
	notices := f["notices"].([]string)
	if len(notices) != 3 || notices[0] != "line 3" || notices[2] != "line 5" {
		t.Errorf("notices = %v", notices)
	}
	if f["noticesTruncated"] != 2 {
		t.Errorf("noticesTruncated = %v", f["noticesTruncated"])
	}
}

func TestNoticeBuffer_NoTruncationMarkerWhenWithinCap(t *testing.T) {
	b := &noticeBuffer{max: 10}
	b.add("only line", "", "")

	f := b.fields()
	if _, present := f["noticesTruncated"]; present {
		t.Error("noticesTruncated should be absent when nothing was dropped")
	}
	if notices := f["notices"].([]string); len(notices) != 1 {
		t.Errorf("notices = %v", notices)
	}
}
