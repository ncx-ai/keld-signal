package spool

import (
	"testing"
	"time"
)

func TestStatsReportsDepthBytesAndOldest(t *testing.T) {
	setHome(t)
	before := time.Now().UnixNano()
	Write(inlinePtr("A", "hello"))
	Write(inlinePtr("B", "world"))
	st, err := Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Rows != 2 {
		t.Fatalf("rows = %d, want 2", st.Rows)
	}
	if st.Bytes <= 0 {
		t.Fatalf("bytes = %d, want > 0", st.Bytes)
	}
	if st.OldestUnixNano < before {
		t.Fatalf("oldest timestamp %d predates the first write %d", st.OldestUnixNano, before)
	}
}

func TestStatsOnEmptySpool(t *testing.T) {
	setHome(t)
	st, err := Stats()
	if err != nil {
		t.Fatalf("empty spool must not error: %v", err)
	}
	if st.Rows != 0 || st.Bytes != 0 || st.OldestUnixNano != 0 {
		t.Fatalf("empty spool stats = %+v, want zeros", st)
	}
}
