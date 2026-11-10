// Package akhcheck reads the course-detail JSON returned by Akhcheck.
package akhcheck

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Assignment struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Deadline          *string    `json:"deadline"`
	EffectiveDeadline *string    `json:"effectiveDeadline"`
	DueDate           *time.Time `json:"-"`
}

type Course struct {
	ID    int64        `json:"id"`
	Name  string       `json:"name"`
	Tasks []Assignment `json:"tasks"`
}

// ParseCourse accepts the JSON response of GET /api/teaching/course/<id>.
func ParseCourse(data []byte) (Course, error) {
	var course Course
	if err := json.Unmarshal(data, &course); err != nil {
		return Course{}, fmt.Errorf("invalid course JSON: %w", err)
	}
	if course.ID <= 0 || course.Tasks == nil {
		return Course{}, fmt.Errorf("course id and tasks array are required")
	}
	seen := make(map[int64]bool, len(course.Tasks))
	for index := range course.Tasks {
		assignment := &course.Tasks[index]
		assignment.Name = strings.TrimSpace(assignment.Name)
		if assignment.ID <= 0 || assignment.Name == "" || seen[assignment.ID] {
			return Course{}, fmt.Errorf("assignment %d: missing or duplicate id or name", index+1)
		}
		seen[assignment.ID] = true
		deadline := assignment.EffectiveDeadline
		if deadline == nil || *deadline == "" {
			deadline = assignment.Deadline
		}
		if deadline != nil && *deadline != "" {
			if len(*deadline) < len(time.DateOnly) {
				return Course{}, fmt.Errorf("assignment %d: invalid deadline", assignment.ID)
			}
			if len(*deadline) > len(time.DateOnly) {
				if _, err := time.Parse(time.RFC3339, *deadline); err != nil {
					return Course{}, fmt.Errorf("assignment %d: invalid deadline", assignment.ID)
				}
			}
			due, err := time.Parse(time.DateOnly, (*deadline)[:len(time.DateOnly)])
			if err != nil {
				return Course{}, fmt.Errorf("assignment %d: invalid deadline", assignment.ID)
			}
			assignment.DueDate = &due
		}
	}
	return course, nil
}
