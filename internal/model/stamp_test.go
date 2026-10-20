package model

import (
	"bytes"
	"sort"
	"strings"
	"testing"
)

func TestStampStringRoundTrip(t *testing.T) {
	stamp := Stamp{WallMillis: 1761055200123, Counter: 7, Device: "d-4a2e"}
	if got := stamp.String(); got != "1761055200123.0007.d-4a2e" {
		t.Fatalf("String() = %q", got)
	}
	parsed, err := ParseStamp(stamp.String())
	if err != nil || parsed != stamp {
		t.Fatalf("ParseStamp = %v, %v", parsed, err)
	}
}

func TestStampLexicalOrderMatchesClockOrder(t *testing.T) {
	stamps := []Stamp{
		{WallMillis: 2, Counter: 0, Device: "d-a"},
		{WallMillis: 1, Counter: 9, Device: "d-b"},
		{WallMillis: 1, Counter: 9, Device: "d-a"},
		{WallMillis: 1, Counter: 10, Device: "d-a"},
	}
	byClock := append([]Stamp(nil), stamps...)
	sort.Slice(byClock, func(i, j int) bool { return byClock[i].Compare(byClock[j]) < 0 })
	byText := append([]Stamp(nil), stamps...)
	sort.Slice(byText, func(i, j int) bool { return byText[i].String() < byText[j].String() })
	for i := range byClock {
		if byClock[i] != byText[i] {
			t.Fatalf("order differs at %d: clock %v text %v", i, byClock[i], byText[i])
		}
	}
}

func TestStampAfter(t *testing.T) {
	older := Stamp{WallMillis: 5, Counter: 1, Device: "d-a"}
	newer := Stamp{WallMillis: 5, Counter: 1, Device: "d-b"}
	if !newer.After(older) || older.After(newer) || older.After(older) {
		t.Fatal("After does not break ties by device")
	}
}

func TestParseStampRejectsGarbage(t *testing.T) {
	for _, text := range []string{"x", "1.2", "a.1.d", "1.b.d"} {
		if _, err := ParseStamp(text); err == nil {
			t.Errorf("ParseStamp(%q) succeeded", text)
		}
	}
}

func TestNewIDs(t *testing.T) {
	random := bytes.NewReader(bytes.Repeat([]byte{0xab}, 64))
	oid, err := NewObjectID(random)
	if err != nil || len(oid) != 32 {
		t.Fatalf("NewObjectID = %q, %v", oid, err)
	}
	device, err := NewDeviceID(random)
	if err != nil || !strings.HasPrefix(string(device), "d-") || len(device) != 18 {
		t.Fatalf("NewDeviceID = %q, %v", device, err)
	}
	if _, err := NewObjectID(bytes.NewReader(nil)); err == nil {
		t.Fatal("NewObjectID with empty random succeeded")
	}
}

func TestStampJSONText(t *testing.T) {
	stamp := Stamp{WallMillis: 12, Counter: 3, Device: "d-x"}
	text, err := stamp.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var back Stamp
	if err := back.UnmarshalText(text); err != nil || back != stamp {
		t.Fatalf("UnmarshalText = %v, %v", back, err)
	}
	if text, _ := (Stamp{}).MarshalText(); len(text) != 0 {
		t.Fatalf("zero stamp text = %q", text)
	}
}
