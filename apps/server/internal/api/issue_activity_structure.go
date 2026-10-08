package api

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
)

// The issue_activity events of the issue-structure views: links
// (create/update/delete_link_activity) and relations
// (create/delete_issue_relation_activity). Sub-issues send
// issue.activity.updated, which track_parent handles.

// structureActivity dispatches the link and relation events.
func (t *activityTask) structureActivity(ctx context.Context) error {
	t.requested = loadDict(t.j.RequestedData)
	t.current = loadDict(t.j.CurrentInstance)
	switch t.j.Type {
	case "link.activity.created":
		t.add(&activity{Comment: "created a link", Verb: "created", Field: strp("link"),
			NewValue: t.requested.strOr("url", ""), NewIdentifier: t.requested.uuid("id")})
	case "link.activity.updated":
		cv, cok := t.current.get("url")
		rv, rok := t.requested.get("url")
		if !pyEqual(cv, cok, rv, rok) {
			t.add(&activity{Comment: "updated a link", Verb: "updated", Field: strp("link"),
				OldValue: t.current.strOr("url", ""), OldIdentifier: t.current.uuid("id"),
				NewValue: t.requested.strOr("url", ""), NewIdentifier: t.current.uuid("id")})
		}
	case "link.activity.deleted":
		t.add(&activity{Comment: "deleted the link", Verb: "deleted", Field: strp("link"),
			OldValue: t.current.strOr("url", ""), NewValue: strp("")})
	case "issue_relation.activity.created":
		return t.relationCreated(ctx)
	case "issue_relation.activity.deleted":
		return t.relationDeleted(ctx)
	}
	return nil
}

// strOr is dict.get(key, def) stored in a TextField: missing is def,
// None stays None.
func (p *pyDict) strOr(key, def string) *string {
	v, ok := p.get(key)
	if !ok {
		return &def
	}
	if v.IsNull() {
		return nil
	}
	return strp(drf.PyStr(v))
}

// uuid is dict.get(key) for a UUIDField that is set from a serializer's id.
func (p *pyDict) uuid(key string) *uuid.UUID {
	v, ok := p.get(key)
	if !ok || !v.IsString() {
		return nil
	}
	id, ok := drf.ParseUUID(v.Str())
	if !ok {
		return nil
	}
	return &id
}

// issueRef is Issue.objects.get(pk=value) read as
// f"{issue.project.identifier}-{issue.sequence_id}"; a miss or a value
// UUIDField rejects ends the task.
func (t *activityTask) issueRef(ctx context.Context, v drf.Value) (uuid.UUID, string, error) {
	ids, err := modelUUIDs([]drf.Value{v})
	if err != nil || len(ids) == 0 {
		return uuid.Nil, "", abort("Issue.objects.get(pk=%s)", drf.PyRepr(v))
	}
	var (
		ident string
		seq   int
	)
	err = t.a.db.QueryRow(ctx, `SELECT p.identifier, i.sequence_id FROM issues i JOIN projects p ON p.id = i.project_id
		WHERE i.id = $1 AND i.deleted_at IS NULL`, ids[0]).Scan(&ident, &seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", abort("Issue %s DoesNotExist", ids[0])
	}
	if err != nil {
		return uuid.Nil, "", err
	}
	return ids[0], ident + "-" + strconv.Itoa(seq), nil
}

// addFor appends an activity of another issue than the task's.
func (t *activityTask) addFor(issue uuid.UUID, act *activity) {
	act.IssueID = &issue
	act.ActorID = &t.actor
	t.activities = append(t.activities, act)
}

// relationType is requested_data.get("relation_type") as the activity
// stores it (field) and formats it (comment).
func (t *activityTask) relationType() (*string, string) {
	field := t.requested.str("relation_type")
	return field, pyStrOrNone(field)
}

// inverseRelation is get_inverse_relation.
func inverseRelation(rel string) string {
	switch rel {
	case "start_after":
		return "start_before"
	case "finish_after":
		return "finish_before"
	case "blocked_by":
		return "blocking"
	case "blocking":
		return "blocked_by"
	case "start_before":
		return "start_after"
	case "finish_before":
		return "finish_after"
	case "implemented_by":
		return "implements"
	case "implements":
		return "implemented_by"
	}
	return rel
}

// relationCreated is create_issue_relation_activity: for each requested
// issue (whether or not the view related it) an activity on both sides.
func (t *activityTask) relationCreated(ctx context.Context) error {
	if t.current != nil {
		return nil
	}
	issues, ok := t.requested.get("issues")
	if !ok || issues.IsNull() {
		return nil
	}
	items, err := pyIter(issues)
	if err != nil {
		return err
	}
	field, rel := t.relationType()
	for _, item := range items {
		related, ref, err := t.issueRef(ctx, item)
		if err != nil {
			return err
		}
		t.add(&activity{Verb: "updated", OldValue: strp(""), NewValue: &ref, Field: field,
			Comment: "added " + rel + " relation", OldIdentifier: &related})
		inverse := inverseRelation(rel)
		var inverseField *string
		if field != nil {
			inverseField = &inverse
		}
		_, self, err := t.issueRef(ctx, drf.JSONValue(jsonString(t.issueID.String())))
		if err != nil {
			return err
		}
		t.addFor(related, &activity{Verb: "updated", OldValue: strp(""), NewValue: &self, Field: inverseField,
			Comment: "added " + inverse + " relation", OldIdentifier: t.issueID})
	}
	return nil
}

// relationDeleted is delete_issue_relation_activity. Both activities keep
// the related issue as old_identifier and the requested type in the
// comment; only blocked_by and blocking are inverted for the other side.
func (t *activityTask) relationDeleted(ctx context.Context) error {
	v, ok := t.requested.get("related_issue")
	if !ok {
		return abort("Issue.objects.get(pk=None)")
	}
	related, ref, err := t.issueRef(ctx, v)
	if err != nil {
		return err
	}
	field, rel := t.relationType()
	comment := "deleted " + rel + " relation"
	t.add(&activity{Verb: "deleted", OldValue: &ref, NewValue: strp(""), Field: field, Comment: comment,
		OldIdentifier: &related})
	_, self, err := t.issueRef(ctx, drf.JSONValue(jsonString(t.issueID.String())))
	if err != nil {
		return err
	}
	other := field
	if field != nil {
		switch *field {
		case "blocked_by":
			other = strp("blocking")
		case "blocking":
			other = strp("blocked_by")
		}
	}
	t.addFor(related, &activity{Verb: "deleted", OldValue: &self, NewValue: strp(""), Field: other, Comment: comment,
		OldIdentifier: &related})
	return nil
}

// pyIter is iterating a json.loads'ed value: a list's items, a string's
// characters, a dict's keys; anything else raises TypeError.
func pyIter(v drf.Value) ([]drf.Value, error) {
	switch v.Kind() {
	case '[':
		items, _ := v.Elems()
		return items, nil
	case '"':
		var out []drf.Value
		for _, r := range v.Str() {
			out = append(out, drf.JSONValue(jsonString(string(r))))
		}
		return out, nil
	case '{':
		var out []drf.Value
		for _, k := range drf.DataFromJSON(v.Raw()).Keys() {
			out = append(out, drf.JSONValue(jsonString(k)))
		}
		return out, nil
	}
	return nil, abort("iterating %s", drf.PyRepr(v))
}
