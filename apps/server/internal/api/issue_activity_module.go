package api

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
)

// The issue_activity events of the module-issue views:
// create_module_issue_activity and delete_module_issue_activity. Neither
// sends notifications (the notifications task skips module events).

// moduleActivity dispatches module.activity.created / .deleted.
func (t *activityTask) moduleActivity(ctx context.Context) error {
	t.requested = loadDict(t.j.RequestedData)
	t.current = loadDict(t.j.CurrentInstance)
	// module_id goes into a UUIDField (new_identifier / old_identifier):
	// a value it rejects fails the bulk_create, and with it the task.
	raw, _ := t.requested.get("module_id")
	ids, err := modelUUIDs([]drf.Value{raw})
	if err != nil {
		return abort("UUIDField(%s)", drf.PyRepr(raw))
	}
	var moduleID *uuid.UUID
	if len(ids) > 0 {
		moduleID = &ids[0]
	}
	switch t.j.Type {
	case "module.activity.created":
		// Module.objects.filter(pk=module_id).first(): a live module's name,
		// or "".
		name := ""
		if moduleID != nil {
			err := t.a.db.QueryRow(ctx, `SELECT name FROM modules WHERE id = $1 AND deleted_at IS NULL`, *moduleID).Scan(&name)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		t.add(&activity{Verb: "created", OldValue: strp(""), NewValue: strp(name), Field: strp("modules"),
			Comment: "added module " + name, NewIdentifier: moduleID})
	case "module.activity.deleted":
		name := t.current.str("module_name")
		comment := "None"
		if name != nil {
			comment = *name
		}
		t.add(&activity{Verb: "deleted", OldValue: name, NewValue: strp(""), Field: strp("modules"),
			Comment: "removed this issue from " + comment, OldIdentifier: moduleID})
	}
	return nil
}
