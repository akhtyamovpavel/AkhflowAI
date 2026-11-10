package recurring

import (
	"context"
	"testing"
	"time"
)

func TestRecurringCalendarAcrossTimezones(t *testing.T) {
	cases := []struct {
		name     string
		zone     string
		year     int
		month    time.Month
		day      int
		hour     int
		due      string
		interval string
		want     string
	}{
		{"utc-daily", "UTC", 2030, time.January, 2, 0, "2030-01-01", "daily", "2030-01-03"},
		{"moscow-before-utc-midnight", "Europe/Moscow", 2030, time.January, 2, 0, "2030-01-01", "daily", "2030-01-03"},
		{"moscow-due-today-before-utc-midnight", "Europe/Moscow", 2030, time.January, 2, 0, "2030-01-02", "daily", "2030-01-03"},
		{"los-angeles-after-utc-midnight", "America/Los_Angeles", 2030, time.January, 2, 23, "2030-01-02", "daily", "2030-01-03"},
		{"spring-forward", "America/Los_Angeles", 2027, time.March, 14, 1, "2027-03-14", "daily", "2027-03-15"},
		{"fall-back-weekly", "America/Los_Angeles", 2027, time.November, 7, 1, "2027-11-07", "weekly", "2027-11-14"},
		{"sydney-weekly", "Australia/Sydney", 2027, time.April, 4, 1, "2027-04-04", "weekly", "2027-04-11"},
		{"monthly-end-of-month", "UTC", 2030, time.January, 31, 12, "2030-01-31", "monthly", "2030-02-28"},
		{"monthly-leap-year", "Europe/Moscow", 2028, time.January, 31, 12, "2028-01-31", "monthly", "2028-02-29"},
		{"monthly-year-boundary", "America/Los_Angeles", 2029, time.December, 31, 23, "2029-12-31", "monthly", "2030-01-31"},
		{"monthly-skips-past-intervals", "UTC", 2030, time.March, 1, 12, "2030-01-31", "monthly", "2030-03-28"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSchedulerFixture(t)
			ctx := context.Background()
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(tc.year, tc.month, tc.day, tc.hour, 30, 0, 0, loc)
			future := now.AddDate(0, 0, 1).Format(time.DateOnly)
			_, err = f.pool.Exec(ctx, `UPDATE tasks SET due_date=$1::date, recurring=$2 WHERE id=$3`, future, tc.interval, f.master)
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.db.ListRecurringOverdue(ctx, now)
			if err != nil || len(before) != 0 {
				t.Fatalf("future task selection: %d, %v", len(before), err)
			}
			unexpected, err := f.db.CreateScheduledOccurrence(ctx, f.master, now)
			if err != nil || unexpected != nil {
				t.Fatalf("future occurrence: %+v, %v", unexpected, err)
			}
			_, err = f.pool.Exec(ctx, `UPDATE tasks SET due_date=$1::date, recurring=$2 WHERE id=$3`, tc.due, tc.interval, f.master)
			if err != nil {
				t.Fatal(err)
			}
			overdue, err := f.db.ListRecurringOverdue(ctx, now)
			if err != nil || len(overdue) != 1 {
				t.Fatalf("due task selection: %d, %v", len(overdue), err)
			}
			occurrence, err := f.db.CreateScheduledOccurrence(ctx, f.master, now)
			if err != nil || occurrence == nil {
				t.Fatalf("occurrence: %+v, %v", occurrence, err)
			}
			var actualDue, actualOccurrence time.Time
			err = f.pool.QueryRow(ctx, `SELECT due_date FROM tasks WHERE id=$1`, f.master).Scan(&actualDue)
			if err != nil {
				t.Fatal(err)
			}
			err = f.pool.QueryRow(ctx, `SELECT due_date FROM tasks WHERE id=$1`, occurrence.ID).Scan(&actualOccurrence)
			if err != nil {
				t.Fatal(err)
			}
			if actualDue.Format(time.DateOnly) != tc.want {
				t.Errorf("next due = %s, want %s", actualDue.Format(time.DateOnly), tc.want)
			}
			if actualOccurrence.Format(time.DateOnly) != now.Format(time.DateOnly) {
				t.Errorf("occurrence date = %s, want %s", actualOccurrence.Format(time.DateOnly), now.Format(time.DateOnly))
			}
		})
	}
}
