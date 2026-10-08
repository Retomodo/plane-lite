package api

import (
	"context"
	"errors"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
)

// The issue_activity events of the cycle views:
// create_cycle_issue_activity and delete_cycle_issue_activity.

// cycleActivity dispatches the cycle events.
func (t *activityTask) cycleActivity(ctx context.Context) error {
	t.requested = loadDict(t.j.RequestedData)
	t.current = loadDict(t.j.CurrentInstance)
	if t.j.Type == "cycle.activity.created" {
		return t.cycleIssuesAdded(ctx)
	}
	return t.cycleIssuesRemoved(ctx)
}

// cycleName is Cycle.objects.filter(pk=...).first(): its id and name, or
// ok false.
func (t *activityTask) cycleName(ctx context.Context, v drf.Value, has bool) (uuid.UUID, string, bool, error) {
	if !has || v.IsNull() {
		return uuid.Nil, "", false, nil
	}
	ids, err := modelUUIDs([]drf.Value{v})
	if err != nil || len(ids) == 0 {
		return uuid.Nil, "", false, abort("Cycle.objects.filter(pk=%s)", drf.PyRepr(v))
	}
	var name string
	err = t.a.db.QueryRow(ctx, `SELECT name FROM cycles WHERE id = $1 AND deleted_at IS NULL`, ids[0]).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", false, nil
	}
	if err != nil {
		return uuid.Nil, "", false, err
	}
	return ids[0], name, true, nil
}

// cycleTouchIssue is the trackers' issue.save(update_fields=["updated_at"])
// on the issue, when it is live; it returns the issue's id.
func (t *activityTask) cycleTouchIssue(ctx context.Context, v drf.Value) (uuid.UUID, error) {
	ids, err := modelUUIDs([]drf.Value{v})
	if err != nil || len(ids) == 0 {
		return uuid.Nil, abort("Issue.objects.filter(pk=%s)", drf.PyRepr(v))
	}
	_, err = t.a.db.Exec(ctx, `UPDATE issues SET updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, ids[0])
	return ids[0], err
}

// cycleIssuesAdded is create_cycle_issue_activity: an "updated" activity
// per issue moved from another cycle, a "created" one per issue added.
// created_cycle_issues must be JSON text (transfer passes a list, which
// ends the task).
func (t *activityTask) cycleIssuesAdded(ctx context.Context) error {
	if t.current == nil {
		return abort("None.get")
	}
	var updated []drf.Value
	if v, ok := t.current.get("updated_cycle_issues"); ok {
		elems, isList := v.Elems()
		if !isList {
			return abort("iterating updated_cycle_issues")
		}
		updated = elems
	}
	createdV, ok := t.current.get("created_cycle_issues")
	if !ok || !createdV.IsString() {
		return abort("json.loads(%s)", drf.PyRepr(createdV))
	}
	created, isList := drf.JSONValue(jsontext.Value(createdV.Str())).Elems()
	if !isList {
		return abort("iterating created_cycle_issues")
	}
	for _, u := range updated {
		rec := &pyDict{d: drf.DataFromJSON(u.Raw())}
		ov, ook := rec.get("old_cycle_id")
		oldID, oldName, oldOK, err := t.cycleName(ctx, ov, ook)
		if err != nil {
			return err
		}
		nv, nok := rec.get("new_cycle_id")
		newID, newName, newOK, err := t.cycleName(ctx, nv, nok)
		if err != nil {
			return err
		}
		iv, _ := rec.get("issue_id")
		issue, err := t.cycleTouchIssue(ctx, iv)
		if err != nil {
			return err
		}
		act := &activity{Verb: "updated", Field: strp("cycles"), OldValue: strp(oldName), NewValue: strp(newName),
			Comment: "updated cycle from " + oldName + "\n                to " + newName}
		if oldOK {
			act.OldIdentifier = &oldID
		}
		if newOK {
			act.NewIdentifier = &newID
		}
		t.addFor(issue, act)
	}
	for _, c := range created {
		rec := &pyDict{d: drf.DataFromJSON(c.Raw())}
		fv, ok := rec.get("fields")
		if !ok || fv.Kind() != '{' {
			return abort("None.get")
		}
		fields := &pyDict{d: drf.DataFromJSON(fv.Raw())}
		cv, cok := fields.get("cycle")
		cycleID, name, found, err := t.cycleName(ctx, cv, cok)
		if err != nil {
			return err
		}
		if !found {
			return abort("None.name")
		}
		iv, _ := fields.get("issue")
		issue, err := t.cycleTouchIssue(ctx, iv)
		if err != nil {
			return err
		}
		t.addFor(issue, &activity{Verb: "created", Field: strp("cycles"), OldValue: strp(""), NewValue: strp(name),
			Comment: "added cycle " + name, NewIdentifier: &cycleID})
	}
	return nil
}

// cycleIssuesRemoved is delete_cycle_issue_activity: a "deleted" activity
// per issue, naming the cycle (or the cycle_name sent, once it is gone).
func (t *activityTask) cycleIssuesRemoved(ctx context.Context) error {
	if t.requested == nil {
		return abort("None.get")
	}
	cv, cok := t.requested.get("cycle_id")
	_, name, found, err := t.cycleName(ctx, cv, cok)
	if err != nil {
		return err
	}
	if !found {
		name = ""
		if v, ok := t.requested.get("cycle_name"); ok {
			name = drf.PyStr(v)
		}
	}
	var oldIdentifier *uuid.UUID
	if cok && !cv.IsNull() {
		if ids, err := modelUUIDs([]drf.Value{cv}); err == nil && len(ids) == 1 {
			oldIdentifier = &ids[0]
		}
	}
	iv, _ := t.requested.get("issues")
	issues, isList := iv.Elems()
	if !isList {
		return abort("iterating issues")
	}
	for _, v := range issues {
		issue, err := t.cycleTouchIssue(ctx, v)
		if err != nil {
			return err
		}
		t.addFor(issue, &activity{Verb: "deleted", Field: strp("cycles"), OldValue: strp(name), NewValue: strp(""),
			Comment: "removed this issue from " + name, OldIdentifier: oldIdentifier})
	}
	return nil
}
