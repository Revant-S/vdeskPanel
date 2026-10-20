package model

import (
	"fmt"
	"strconv"
	"strings"
)

// Stamp is a hybrid logical clock reading. Its string form sorts lexically in clock order.
type Stamp struct {
	WallMillis int64
	Counter    uint16
	Device     DeviceID
}

func (s Stamp) String() string {
	return fmt.Sprintf("%013d.%04d.%s", s.WallMillis, s.Counter, s.Device)
}

func (s Stamp) IsZero() bool {
	return s == Stamp{}
}

func (s Stamp) After(other Stamp) bool {
	return s.Compare(other) > 0
}

func (s Stamp) Compare(other Stamp) int {
	switch {
	case s.WallMillis != other.WallMillis:
		return compareOrdered(s.WallMillis, other.WallMillis)
	case s.Counter != other.Counter:
		return compareOrdered(s.Counter, other.Counter)
	default:
		return strings.Compare(string(s.Device), string(other.Device))
	}
}

func ParseStamp(text string) (Stamp, error) {
	if text == "" {
		return Stamp{}, nil
	}
	parts := strings.SplitN(text, ".", 3)
	if len(parts) != 3 {
		return Stamp{}, fmt.Errorf("parse stamp %q: want wall.counter.device", text)
	}
	wall, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Stamp{}, fmt.Errorf("parse stamp wall: %w", err)
	}
	counter, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return Stamp{}, fmt.Errorf("parse stamp counter: %w", err)
	}
	return Stamp{WallMillis: wall, Counter: uint16(counter), Device: DeviceID(parts[2])}, nil
}

func compareOrdered[T int64 | uint16](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
