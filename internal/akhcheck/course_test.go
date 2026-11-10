package akhcheck

import "testing"

func TestParseCourse(t *testing.T) {
	input := `{"id":117,"tasks":[{"id":4,"name":"  Bash  ","description":"Text","deadline":"2026-10-05T23:59:00+03:00","effectiveDeadline":"2026-10-07T23:59:00+03:00"},{"id":5,"name":"Git","deadline":null}]}`
	course, err := ParseCourse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if course.ID != 117 || len(course.Tasks) != 2 || course.Tasks[0].Name != "Bash" {
		t.Fatalf("unexpected course: %+v", course)
	}
	if course.Tasks[0].DueDate.Format("2006-01-02") != "2026-10-07" {
		t.Fatalf("incorrect effective deadline: %+v", course.Tasks[0].DueDate)
	}
	if course.Tasks[1].DueDate != nil {
		t.Fatalf("unexpected deadline: %+v", course.Tasks[1].DueDate)
	}
}

func TestParseCourseRejectsInvalidExports(t *testing.T) {
	cases := []string{
		`{}`, `{"id":1}`, `{"id":1,"tasks":[{"id":0,"name":"A"}]}`,
		`{"id":1,"tasks":[{"id":1,"name":" "}]}`,
		`{"id":1,"tasks":[{"id":1,"name":"A"},{"id":1,"name":"B"}]}`,
		`{"id":1,"tasks":[{"id":1,"name":"A","deadline":"bad"}]}`,
		`{"id":1,"tasks":[{"id":1,"name":"A","deadline":"2026-01-01invalid"}]}`,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseCourse([]byte(input)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
