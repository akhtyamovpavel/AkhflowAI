package domain

import (
	"testing"
	"time"
)

func TestMonthlyRecurrenceUsesExistingDay(t *testing.T) {
	cases := []struct {
		from string
		want string
	}{
		{"2030-01-31", "2030-02-28"},
		{"2028-01-31", "2028-02-29"},
		{"2030-03-31", "2030-04-30"},
		{"2030-04-30", "2030-05-30"},
		{"2030-12-31", "2031-01-31"},
	}
	step, ok := RecurrenceStep("monthly")
	if !ok {
		t.Fatal("monthly recurrence unavailable")
	}
	for _, tc := range cases {
		t.Run(tc.from, func(t *testing.T) {
			date, err := time.Parse(time.DateOnly, tc.from)
			if err != nil {
				t.Fatal(err)
			}
			actual := step(date).Format(time.DateOnly)
			if actual != tc.want {
				t.Errorf("next date = %s, want %s", actual, tc.want)
			}
		})
	}
}
