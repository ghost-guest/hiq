package calendar

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

// ExportICS writes events to an .ics file (RFC 5545).
func ExportICS(path string, events []Event) error {
	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	cal.SetCalscale("GREGORIAN")
	cal.SetXWRCalName("Hiq Calendar")

	for _, e := range events {
		event := cal.AddEvent(e.ID)
		event.SetSummary(e.Title)
		event.SetStartAt(e.StartTime)
		event.SetEndAt(e.EndTime)
		if e.Description != "" {
			event.SetDescription(e.Description)
		}
		if e.Location != "" {
			event.SetLocation(e.Location)
		}
		if e.AllDay {
			event.SetAllDayStartAt(e.StartTime)
			event.SetAllDayEndAt(e.EndTime)
		}
		if e.Recurrence != "" {
			event.SetProperty(ics.ComponentPropertyRrule, e.Recurrence)
		}
		for _, m := range e.Reminders {
			alarm := event.AddAlarm()
			alarm.SetAction(ics.ActionDisplay)
			alarm.SetDescription("Reminder")
			alarm.SetTrigger(fmt.Sprintf("-PT%dM", m))
		}
		for _, tag := range e.Tags {
			event.AddProperty(ics.ComponentPropertyCategories, tag)
		}
		event.SetDtStampTime(e.CreatedAt)
	}

	var buf bytes.Buffer
	if err := cal.SerializeTo(&buf); err != nil {
		return fmt.Errorf("ics serialize: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ImportICS reads an .ics file and returns parsed events.
func ImportICS(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ics read: %w", err)
	}
	return ParseICS(string(data))
}

// ParseICS parses .ics content and returns events.
func ParseICS(content string) ([]Event, error) {
	cal, err := ics.ParseCalendar(strings.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("ics parse: %w", err)
	}

	var events []Event
	for _, component := range cal.Events() {
		e := Event{
			ID:     component.Id(),
			Source: "ics",
		}

		if summary := component.GetProperty(ics.ComponentPropertySummary); summary != nil {
			e.Title = summary.Value
		}
		if desc := component.GetProperty(ics.ComponentPropertyDescription); desc != nil {
			e.Description = desc.Value
		}
		if loc := component.GetProperty(ics.ComponentPropertyLocation); loc != nil {
			e.Location = loc.Value
		}

		// Parse start/end times
		if start, err := component.GetStartAt(); err == nil {
			e.StartTime = start
		}
		if end, err := component.GetEndAt(); err == nil {
			e.EndTime = end
		}

		// All-day detection
		if dtstart := component.GetProperty(ics.ComponentPropertyDtStart); dtstart != nil {
			if dtstart.ICalParameters != nil {
				if vals, ok := dtstart.ICalParameters["VALUE"]; ok && len(vals) > 0 && vals[0] == "DATE" {
					e.AllDay = true
				}
			}
		}

		// RRULE
		if rrule := component.GetProperty(ics.ComponentPropertyRrule); rrule != nil {
			e.Recurrence = rrule.Value
		}

		// Categories (tags)
		if cat := component.GetProperty(ics.ComponentPropertyCategories); cat != nil && cat.Value != "" {
			e.Tags = append(e.Tags, cat.Value)
		}

		// Alarms (reminders)
		for _, alarm := range component.Alarms() {
			if trigger := alarm.GetProperty(ics.ComponentPropertyTrigger); trigger != nil {
				m := parseTriggerMinutes(trigger.Value)
				if m > 0 {
					e.Reminders = append(e.Reminders, m)
				}
			}
		}

		if e.Title == "" {
			e.Title = "(无标题)"
		}
		if e.StartTime.IsZero() {
			continue // skip events without start time
		}
		if e.EndTime.IsZero() {
			e.EndTime = e.StartTime.Add(time.Hour)
		}
		if e.Timezone == "" {
			e.Timezone = "Asia/Shanghai"
		}
		if e.Status == "" {
			e.Status = "confirmed"
		}

		events = append(events, e)
	}
	return events, nil
}

// parseTriggerMinutes parses an iCal trigger value like "-PT15M" into minutes.
func parseTriggerMinutes(s string) int {
	s = strings.TrimSpace(s)
	// Format: -PT15M, -PT1H, -P1D
	if !strings.HasPrefix(s, "-P") {
		return 0
	}
	s = strings.TrimPrefix(s, "-P")
	s = strings.TrimPrefix(s, "T")

	if strings.HasSuffix(s, "M") {
		s = strings.TrimSuffix(s, "M")
		return atoi(s)
	}
	if strings.HasSuffix(s, "H") {
		s = strings.TrimSuffix(s, "H")
		return atoi(s) * 60
	}
	if strings.HasSuffix(s, "D") {
		s = strings.TrimSuffix(s, "D")
		return atoi(s) * 1440
	}
	return 0
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
