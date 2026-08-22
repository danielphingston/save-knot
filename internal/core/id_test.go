package core

import (
	"testing"
	"time"
)

func TestNewIDIsTimeSortableAndUnique(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	first, err := NewID(now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewID(now)
	if err != nil {
		t.Fatal(err)
	}
	later, err := NewID(now.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("random ID collision")
	}
	if later <= first || later <= second {
		t.Fatalf("IDs are not time-sortable: first=%q second=%q later=%q", first, second, later)
	}
}
