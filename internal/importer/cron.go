package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func (i *Importer) collectCron(root string, c *collected) error {
	path := filepath.Join(root, "cron", "jobs.json")
	raw, err := i.readOptional(path)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return syntaxError(path, err)
	}
	var jobs []any
	switch value := doc.(type) {
	case []any:
		jobs = value
	case map[string]any:
		list, ok := value["jobs"].([]any)
		if !ok {
			return syntaxError(path, errors.New("jobs.json has no jobs list"))
		}
		jobs = list
	default:
		return syntaxError(path, errors.New("jobs.json is not a list or an object with jobs"))
	}
	for index, rawJob := range jobs {
		fields, ok := rawJob.(map[string]any)
		if !ok {
			c.report.skip(SourceCron, fmt.Sprintf("jobs[%d]", index), "job is not an object")
			continue
		}
		i.mapCronJob(fields, index, c)
	}
	return nil
}

func (i *Importer) mapCronJob(fields map[string]any, index int, c *collected) {
	name := childString(fields, "name")
	if name == "" {
		name = childString(fields, "id")
	}
	key := name
	if key == "" {
		key = fmt.Sprintf("jobs[%d]", index)
	}
	if c.reject(SourceCron, key) {
		return
	}
	if childString(fields, "prompt") == "" {
		c.report.skip(SourceCron, key, "job has no prompt (script-only jobs have no Damn-Bot equivalent)")
		return
	}
	schedule, err := parseSchedule(fields["schedule"])
	if err != nil {
		c.report.skip(SourceCron, key, err.Error())
		return
	}
	id := childString(fields, "id")
	if id == "" {
		id = name
	}
	c.jobs = append(c.jobs, CronJob{
		ID:       id,
		Name:     name,
		Prompt:   childString(fields, "prompt"),
		Deliver:  childString(fields, "deliver"),
		Workdir:  childString(fields, "workdir"),
		Schedule: schedule,
		Enabled:  false,
	})
	c.report.item(SourceCron)
}

func parseSchedule(value any) (Schedule, error) {
	switch schedule := value.(type) {
	case string:
		return parseScheduleString(schedule)
	case map[string]any:
		kind := childString(schedule, "kind")
		switch kind {
		case "cron":
			if expr := childString(schedule, "expr"); expr != "" {
				return Schedule{Kind: kind, Expr: expr}, nil
			}
		case "interval":
			if minutes, ok := childInt(schedule, "minutes"); ok && minutes > 0 {
				return Schedule{Kind: kind, Minutes: minutes}, nil
			}
		case "once":
			if at := childString(schedule, "run_at"); at != "" {
				return Schedule{Kind: kind, RunAt: at}, nil
			}
		}
		return Schedule{}, errors.New("schedule kind is not cron, interval or once")
	default:
		return Schedule{}, errors.New("schedule must be a string or a table")
	}
}

func parseScheduleString(value string) (Schedule, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return Schedule{}, errors.New("schedule is empty")
	}
	lower := strings.ToLower(text)
	if rest, ok := strings.CutPrefix(lower, "every "); ok {
		if minutes, ok := intervalMinutes(strings.TrimSpace(rest)); ok {
			return Schedule{Kind: "interval", Minutes: minutes}, nil
		}
	}
	fields := strings.Fields(text)
	if len(fields) >= 5 && len(fields) <= 6 && cronFields(fields[:5]) {
		return Schedule{Kind: "cron", Expr: text}, nil
	}
	if strings.Contains(text, "T") || isDatePrefix(text) {
		return Schedule{Kind: "once", RunAt: text}, nil
	}
	return Schedule{}, errors.New("schedule is not a cron expression, an interval or a timestamp")
}

func intervalMinutes(value string) (int, bool) {
	if len(value) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch value[len(value)-1] {
	case 'm':
		return n, true
	case 'h':
		return n * 60, true
	default:
		return 0, false
	}
}

func cronFields(fields []string) bool {
	for _, field := range fields {
		for _, r := range field {
			if (r < '0' || r > '9') && !strings.ContainsRune("*/,-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", r) {
				return false
			}
		}
	}
	return true
}

func isDatePrefix(text string) bool {
	if len(text) < 10 || text[4] != '-' || text[7] != '-' {
		return false
	}
	for _, index := range []int{0, 1, 2, 3, 5, 6, 8, 9} {
		if text[index] < '0' || text[index] > '9' {
			return false
		}
	}
	return true
}
