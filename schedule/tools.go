package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
)

// Tools returns what lets the agent manage its own tasks when the person
// asks in their own words: schedule_task, list_scheduled_tasks and
// unschedule_task. onChange, when set, is called after a write so a runner
// can refresh at once.
func Tools(store Store, loc *time.Location, onChange func(context.Context)) []agent.Tool {
	if loc == nil {
		loc = time.Local
	}
	return []agent.Tool{&scheduleTool{store, loc, onChange}, &listTool{store, loc}, &unscheduleTool{store, onChange}}
}

type scheduleArgs struct {
	Name   string `json:"name" jsonschema:"description=A short name for the task, in kebab-case. Reusing a name replaces that task."`
	Cron   string `json:"cron" jsonschema:"description=When to run, as five cron fields (minute hour day-of-month month day-of-week) in the person's time zone, or @hourly, @daily, @weekly, @every 2h."`
	Prompt string `json:"prompt" jsonschema:"description=What to ask yourself each time, written so it stands alone: the run has no other context than its own past runs."`
}

type scheduleTool struct {
	store    Store
	loc      *time.Location
	onChange func(context.Context)
}

func (t *scheduleTool) Name() string { return "schedule_task" }
func (t *scheduleTool) Description() string {
	return "Schedule a task to run on its own at given times, without the person asking: a weekly check, a daily digest, a reminder. Use it when the person asks for something to happen regularly or at a later time. The run gets the prompt and its own past runs, nothing else, so write the prompt to stand alone."
}
func (t *scheduleTool) InputSchema() any { return scheduleArgs{} }
func (t *scheduleTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var in scheduleArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return failure("could not parse arguments: %v", err), nil
	}
	task := Task{Name: strings.TrimSpace(in.Name), Cron: strings.TrimSpace(in.Cron), Prompt: strings.TrimSpace(in.Prompt)}
	if err := task.Valid(); err != nil {
		return failure("%v", err), nil
	}
	if err := t.store.Put(ctx, task); err != nil {
		return failure("could not save the task: %v", err), nil
	}
	if t.onChange != nil {
		t.onChange(ctx)
	}
	next, _ := task.Next(time.Now(), t.loc)
	return agent.ToolResult{Content: fmt.Sprintf("scheduled %q (%s); next run %s", task.Name, task.Cron, next.Format("Mon 2006-01-02 15:04 MST"))}, nil
}

type listTool struct {
	store Store
	loc   *time.Location
}

func (t *listTool) Name() string { return "list_scheduled_tasks" }
func (t *listTool) Description() string {
	return "List the tasks scheduled to run on their own, with when each runs next. Use it before changing or removing one, and when the person asks what is planned."
}
func (t *listTool) InputSchema() any { return struct{}{} }
func (t *listTool) Execute(ctx context.Context, _ json.RawMessage) (agent.ToolResult, error) {
	tasks, err := t.store.List(ctx)
	if err != nil {
		return failure("could not read the tasks: %v", err), nil
	}
	if len(tasks) == 0 {
		return agent.ToolResult{Content: "no task is scheduled"}, nil
	}
	var b strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&b, "- %s (%s)", task.Name, task.Cron)
		if task.Paused {
			b.WriteString(" paused")
		} else if next, err := task.Next(time.Now(), t.loc); err == nil {
			fmt.Fprintf(&b, " next %s", next.Format("Mon 2006-01-02 15:04"))
		}
		fmt.Fprintf(&b, ": %s\n", task.Prompt)
	}
	return agent.ToolResult{Content: b.String()}, nil
}

type unscheduleArgs struct {
	Name string `json:"name" jsonschema:"description=The task to remove, by name."`
}

type unscheduleTool struct {
	store    Store
	onChange func(context.Context)
}

func (t *unscheduleTool) Name() string { return "unschedule_task" }
func (t *unscheduleTool) Description() string {
	return "Remove a scheduled task by name, when the person no longer wants it."
}
func (t *unscheduleTool) InputSchema() any { return unscheduleArgs{} }
func (t *unscheduleTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var in unscheduleArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return failure("could not parse arguments: %v", err), nil
	}
	if err := t.store.Remove(ctx, strings.TrimSpace(in.Name)); err != nil {
		return failure("%v", err), nil
	}
	if t.onChange != nil {
		t.onChange(ctx)
	}
	return agent.ToolResult{Content: fmt.Sprintf("removed %q", in.Name)}, nil
}

func failure(format string, args ...any) agent.ToolResult {
	return agent.ToolResult{Content: "error: " + fmt.Sprintf(format, args...), Metadata: map[string]any{"ok": false}}
}
