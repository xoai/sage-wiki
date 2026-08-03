package config

import (
	"testing"
	"time"
)

func TestNowUTC_SourceDateEpoch(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	got := NowUTC()
	want := time.Unix(1700000000, 0).UTC()
	if !got.Equal(want) {
		t.Errorf("NowUTC() = %v, want %v (SOURCE_DATE_EPOCH pinned)", got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("NowUTC() location = %v, want UTC", got.Location())
	}
}

func TestNowUTC_WallClockWithoutSDE(t *testing.T) {
	before := time.Now().UTC()
	got := NowUTC()
	after := time.Now().UTC()
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("NowUTC() = %v, want wall clock between %v and %v", got, before, after)
	}
}

func TestUserNow_SourceDateEpoch(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	c := &CompilerConfig{}
	got := c.UserNow()
	want := time.Unix(1700000000, 0).UTC().Format(time.RFC3339)
	if got != want {
		t.Errorf("UserNow() = %q, want %q (SOURCE_DATE_EPOCH pinned)", got, want)
	}
}
