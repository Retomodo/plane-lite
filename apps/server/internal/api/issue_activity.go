package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/sanitize"
)

// issueActivityJob is the issue_activity Celery task's arguments. It runs
// the notifications task (which it .delay()s) itself.
type issueActivityJob struct {
	Type            string  `json:"type"`
	RequestedData   *string `json:"requested_data"`
	CurrentInstance *string `json:"current_instance"`
	// CurrentIsDict marks a current_instance passed as a dict rather than
	// JSON text (the delete view passes {}), which json.loads refuses.
	CurrentIsDict bool   `json:"current_is_dict,omitempty"`
	IssueID       string `json:"issue_id"`
	ActorID       string `json:"actor_id"`
	ProjectID     string `json:"project_id"`
	Epoch         int64  `json:"epoch"`
	Subscriber    bool   `json:"subscriber"`
	Notification  bool   `json:"notification"`
	// ActorTZ is the request's active timezone, which the reference (Celery
	// eager) still has when serializing activity times.
	ActorTZ string `json:"actor_tz,omitempty"`
}

func (issueActivityJob) Kind() string { return "issue_activity" }

func (a *API) enqueueIssueActivity(ctx context.Context, j issueActivityJob) {
	if p := httpx.PrincipalFrom(ctx); p != nil && p.Timezone != nil && j.ActorTZ == "" {
		j.ActorTZ = p.Timezone.String()
	}
	if err := jobs.Enqueue(ctx, a.jobs, j); err != nil {
		a.log.Error("enqueue issue_activity", "err", err)
	}
}

func (a *API) registerIssueJobs() {
	jobs.Register(a.jobs, func(ctx context.Context, j issueActivityJob) error {
		if err := a.issueActivity(ctx, j); err != nil {
			// The task logs and swallows every exception.
			a.log.Error("issue_activity", "type", j.Type, "err", err)
		}
		return nil
	})
	jobs.Register(a.jobs, func(ctx context.Context, j descriptionVersionJob) error {
		if err := a.issueDescriptionVersion(ctx, j); err != nil {
			a.log.Error("issue_description_version_task", "err", err)
		}
		return nil
	})
}

// errTaskAbort stands for an exception that ends a task early, keeping
// what it already wrote.
type errTaskAbort struct{ why string }

func (e errTaskAbort) Error() string { return "task aborted: " + e.why }

func abort(format string, args ...any) error { return errTaskAbort{fmt.Sprintf(format, args...)} }

// activity is one IssueActivity row.
type activity struct {
	ID             uuid.UUID
	IssueID        *uuid.UUID
	IssueCommentID *uuid.UUID
	Verb           string
	Field          *string
	OldValue       *string
	NewValue       *string
	Comment        string
	OldIdentifier  *uuid.UUID
	NewIdentifier  *uuid.UUID
	ActorID        *uuid.UUID
	CreatedAt      time.Time
}

func strp(s string) *string { return &s }

// pyDict is a json.loads'ed dict.
type pyDict struct {
	d *drf.Data
}

func loadDict(s *string) *pyDict {
	if s == nil {
		return nil
	}
	return &pyDict{d: drf.DataFromJSON(jsontext.Value(*s))}
}

// get is dict.get(key): ok is false for a missing key (None).
func (p *pyDict) get(key string) (drf.Value, bool) {
	if p == nil {
		return drf.Value{}, false
	}
	return p.d.Get(key)
}

// isNone reports dict.get(key) is None.
func (p *pyDict) isNone(key string) bool {
	v, ok := p.get(key)
	return !ok || v.IsNull()
}

// str is str(dict.get(key)) as a TextField stores it: nil for None.
func (p *pyDict) str(key string) *string {
	v, ok := p.get(key)
	if !ok || v.IsNull() {
		return nil
	}
	return strp(drf.PyStr(v))
}

// or is dict.get(a) or dict.get(b).
func (p *pyDict) or(a, b string) (drf.Value, bool) {
	if v, ok := p.get(a); ok && drf.PyTruthy(v) {
		return v, true
	}
	return p.get(b)
}

// pyEqual is Python == between two json.loads values (missing is None).
func pyEqual(a drf.Value, aok bool, b drf.Value, bok bool) bool {
	an, bn := !aok || a.IsNull(), !bok || b.IsNull()
	if an || bn {
		return an == bn
	}
	var av, bv any
	if json.Unmarshal(a.Raw(), &av) != nil || json.Unmarshal(b.Raw(), &bv) != nil {
		return false
	}
	return pyEqualAny(av, bv)
}

func pyEqualAny(a, b any) bool {
	num := func(v any) (float64, bool) {
		switch x := v.(type) {
		case float64:
			return x, true
		case bool:
			if x {
				return 1, true
			}
			return 0, true
		}
		return 0, false
	}
	if an, ok := num(a); ok {
		bn, ok := num(b)
		return ok && an == bn
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !pyEqualAny(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !pyEqualAny(xv, yv) {
				return false
			}
		}
		return true
	}
	return false
}

// isValidUUID4 is plane.utils.uuid.is_valid_uuid on a value already known
// not to be None. uuid.UUID() raises AttributeError (not ValueError) for
// anything but a str, which ends the task.
func isValidUUID4(v drf.Value) (bool, error) {
	if !v.IsString() {
		return false, abort("uuid.UUID(%s)", drf.PyRepr(v))
	}
	u, ok := drf.ParseUUID(v.Str())
	return ok && u.Variant() == uuid.RFC4122 && u.Version() == 4, nil
}

// extractIDs is extract_ids: the str() of each member of data[primary]
// (or data[fallback]), as a set kept in first-seen order.
func extractIDs(p *pyDict, primary, fallback string) ([]drf.Value, error) {
	if p == nil {
		return nil, nil
	}
	v, ok := p.get(primary)
	if !ok {
		v, ok = p.get(fallback)
		if !ok {
			return nil, nil
		}
	}
	elems, isList := v.Elems()
	if !isList {
		if v.IsNull() || v.Kind() == '0' || v.Kind() == 't' || v.Kind() == 'f' {
			return nil, abort("iterating %s", drf.PyRepr(v))
		}
		return nil, abort("extract_ids over %s", drf.PyRepr(v))
	}
	var out []drf.Value
	seen := map[string]bool{}
	for _, e := range elems {
		s := drf.PyStr(e)
		if !seen[s] {
			seen[s] = true
			raw, _ := json.Marshal(s)
			out = append(out, drf.JSONValue(raw))
		}
	}
	return out, nil
}

// activityTask holds one run of issue_activity.
type activityTask struct {
	a           *API
	j           issueActivityJob
	issueID     *uuid.UUID
	projectID   uuid.UUID
	workspaceID uuid.UUID
	actor       uuid.UUID
	activities  []*activity
	// subscribers are IssueSubscriber rows track_assignees bulk-creates.
	requested, current *pyDict
}

func (t *activityTask) add(act *activity) {
	act.IssueID = t.issueID
	if act.ActorID == nil {
		act.ActorID = &t.actor
	}
	t.activities = append(t.activities, act)
}

// issueActivity ports bgtasks.issue_activities_task.issue_activity for the
// issue events (created, updated, deleted).
func (a *API) issueActivity(ctx context.Context, j issueActivityJob) error {
	projectID, err := uuid.Parse(j.ProjectID)
	if err != nil {
		return nil
	}
	actor, err := uuid.Parse(j.ActorID)
	if err != nil {
		return err
	}
	t := &activityTask{a: a, j: j, projectID: projectID, actor: actor}
	if err := a.db.QueryRow(ctx, `SELECT workspace_id FROM projects WHERE id = $1 AND deleted_at IS NULL`, projectID).
		Scan(&t.workspaceID); err != nil {
		return err
	}
	if j.IssueID != "" {
		id, err := uuid.Parse(j.IssueID)
		if err != nil {
			return err
		}
		t.issueID = &id
		// The Redis issue-id -> origin key is not written: the origin is
		// configuration (APP_BASE_URL), read directly where it is needed.
		if _, err := a.db.Exec(ctx, `UPDATE issues SET updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id); err != nil {
			return err
		}
	}
	switch j.Type {
	case "issue.activity.created":
		err = t.created(ctx)
	case "issue.activity.updated":
		err = t.updated(ctx)
	case "issue.activity.deleted":
		comment := "deleted the issue"
		t.add(&activity{Verb: "deleted", Field: strp("issue"), Comment: comment})
	}
	if err != nil {
		return err
	}
	if err := t.insert(ctx); err != nil {
		return err
	}
	if j.Notification {
		return a.notifications(ctx, j, t.activities)
	}
	return nil
}

// insert is IssueActivity.objects.bulk_create: each row stamped as it is
// built, with no created_by.
func (t *activityTask) insert(ctx context.Context) error {
	if len(t.activities) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, act := range t.activities {
		batch.Queue(`INSERT INTO issue_activities (issue_id, issue_comment_id, verb, field, old_value, new_value, comment,
				old_identifier, new_identifier, actor_id, project_id, workspace_id, epoch, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, clock_timestamp(), clock_timestamp())
			RETURNING id, created_at`,
			act.IssueID, act.IssueCommentID, act.Verb, act.Field, act.OldValue, act.NewValue, act.Comment,
			act.OldIdentifier, act.NewIdentifier, act.ActorID, t.projectID, t.workspaceID, float64(t.j.Epoch)).
			QueryRow(func(row pgx.Row) error { return row.Scan(&act.ID, &act.CreatedAt) })
	}
	return pgx.BeginFunc(ctx, t.a.db, func(tx pgx.Tx) error { return tx.SendBatch(ctx, batch).Close() })
}

// created is create_issue_activity.
func (t *activityTask) created(ctx context.Context) error {
	var (
		createdAt time.Time
		createdBy *uuid.UUID
	)
	if err := t.a.db.QueryRow(ctx, `SELECT created_at, created_by_id FROM issues WHERE id = $1 AND deleted_at IS NULL`,
		*t.issueID).Scan(&createdAt, &createdBy); err != nil {
		return err
	}
	// IssueActivity.objects.create(...), then created_at and actor_id taken
	// from the issue.
	if _, err := t.a.db.Exec(ctx, `INSERT INTO issue_activities (issue_id, project_id, workspace_id, comment, verb,
			actor_id, epoch, created_by_id, created_at)
		VALUES ($1, $2, $3, 'created the issue', 'created', $4, $5, $6, $7)`,
		*t.issueID, t.projectID, t.workspaceID, createdBy, float64(t.j.Epoch), t.actor, createdAt); err != nil {
		return err
	}
	t.requested = loadDict(t.j.RequestedData)
	if !t.requested.isNone("assignee_ids") {
		return t.trackAssignees(ctx)
	}
	return nil
}

// updated is update_issue_activity: a tracker per request key, in order.
func (t *activityTask) updated(ctx context.Context) error {
	t.requested = loadDict(t.j.RequestedData)
	t.current = loadDict(t.j.CurrentInstance)
	trackers := map[string]func(context.Context) error{
		"name":             t.simple("name", "updated the name to", false),
		"parent_id":        t.trackParent,
		"priority":         t.simple("priority", "updated the priority to", false),
		"state_id":         t.trackState,
		"description_html": t.trackDescription,
		"target_date":      t.simple("target_date", "updated the target date to", true),
		"start_date":       t.simple("start_date", "updated the start date to ", true),
		"label_ids":        t.trackLabels,
		"assignee_ids":     t.trackAssignees,
		"estimate_point":   t.trackEstimatePoint,
		"archived_at":      t.trackArchivedAt,
		"closed_to":        t.trackClosedTo,
		"parent":           t.trackParent,
		"state":            t.trackState,
		"assignees":        t.trackAssignees,
		"labels":           t.trackLabels,
	}
	for _, key := range t.requested.d.Keys() {
		if fn, ok := trackers[key]; ok {
			if err := fn(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// simple is track_name / track_priority / track_target_date /
// track_start_date: a plain before/after; dates store None as "".
func (t *activityTask) simple(field, comment string, emptyForNone bool) func(context.Context) error {
	return func(context.Context) error {
		cv, cok := t.current.get(field)
		rv, rok := t.requested.get(field)
		if pyEqual(cv, cok, rv, rok) {
			return nil
		}
		old, new := t.current.str(field), t.requested.str(field)
		if emptyForNone {
			if old == nil {
				old = strp("")
			}
			if new == nil {
				new = strp("")
			}
		}
		t.add(&activity{Verb: "updated", OldValue: old, NewValue: new, Field: strp(field), Comment: comment})
		return nil
	}
}

func (t *activityTask) trackDescription(ctx context.Context) error {
	cv, cok := t.current.get("description_html")
	rv, rok := t.requested.get("description_html")
	if pyEqual(cv, cok, rv, rok) {
		return nil
	}
	var (
		lastID    uuid.UUID
		lastField *string
		lastActor *uuid.UUID
	)
	err := t.a.db.QueryRow(ctx, `SELECT id, field, actor_id FROM issue_activities WHERE issue_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, *t.issueID).Scan(&lastID, &lastField, &lastActor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && lastField != nil && *lastField == "description" && lastActor != nil && *lastActor == t.actor {
		_, err := t.a.db.Exec(ctx, `UPDATE issue_activities SET created_at = clock_timestamp() WHERE id = $1`, lastID)
		return err
	}
	t.add(&activity{Verb: "updated", OldValue: t.current.str("description_html"), NewValue: t.requested.str("description_html"),
		Field: strp("description"), Comment: "updated the description to"})
	return nil
}

func (t *activityTask) trackParent(ctx context.Context) error {
	cur, cok := t.current.or("parent_id", "parent")
	req, rok := t.requested.or("parent_id", "parent")
	for _, p := range []struct {
		v  drf.Value
		ok bool
	}{{cur, cok}, {req, rok}} {
		if p.ok && !p.v.IsNull() {
			valid, err := isValidUUID4(p.v)
			if err != nil {
				return err
			}
			if !valid {
				return nil
			}
		}
	}
	if pyEqual(cur, cok, req, rok) {
		return nil
	}
	ref := func(v drf.Value, ok bool) (string, *uuid.UUID, error) {
		if !ok || v.IsNull() {
			return "", nil, nil
		}
		id, _ := drf.ParseUUID(v.Str())
		var ident string
		var seq int
		err := t.a.db.QueryRow(ctx, `SELECT p.identifier, i.sequence_id FROM issues i JOIN projects p ON p.id = i.project_id
			WHERE i.id = $1 AND i.deleted_at IS NULL`, id).Scan(&ident, &seq)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		return ident + "-" + strconv.Itoa(seq), &id, nil
	}
	oldV, oldID, err := ref(cur, cok)
	if err != nil {
		return err
	}
	newV, newID, err := ref(req, rok)
	if err != nil {
		return err
	}
	t.add(&activity{Verb: "updated", OldValue: &oldV, NewValue: &newV, Field: strp("parent"),
		Comment: "updated the parent issue to", OldIdentifier: oldID, NewIdentifier: newID})
	return nil
}

func (t *activityTask) trackState(ctx context.Context) error {
	cur, cok := t.current.or("state_id", "state")
	req, rok := t.requested.or("state_id", "state")
	norm := func(v drf.Value, ok bool) (*string, error) {
		if !ok || v.IsNull() {
			return nil, nil
		}
		valid, err := isValidUUID4(v)
		if err != nil || !valid {
			return nil, err
		}
		s := v.Str()
		return &s, nil
	}
	curID, err := norm(cur, cok)
	if err != nil {
		return err
	}
	reqID, err := norm(req, rok)
	if err != nil {
		return err
	}
	if (curID == nil) == (reqID == nil) && (curID == nil || *curID == *reqID) {
		return nil
	}
	lookup := func(s *string) (*string, *uuid.UUID, error) {
		if s == nil {
			return nil, nil, nil
		}
		id, _ := drf.ParseUUID(*s)
		var name string
		err := t.a.db.QueryRow(ctx, `SELECT name FROM states WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL
			AND "group" <> 'triage'`, id, t.projectID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		return &name, &id, nil
	}
	newName, newID, err := lookup(reqID)
	if err != nil {
		return err
	}
	oldName, oldID, err := lookup(curID)
	if err != nil {
		return err
	}
	t.add(&activity{Verb: "updated", OldValue: oldName, NewValue: newName, Field: strp("state"),
		Comment: "updated the state to", OldIdentifier: oldID, NewIdentifier: newID})
	return nil
}

// diffIDs is the set arithmetic of track_labels and track_assignees.
func (t *activityTask) diffIDs(primary, fallback string) (added, dropped []drf.Value, err error) {
	req, err := extractIDs(t.requested, primary, fallback)
	if err != nil {
		return nil, nil, err
	}
	cur, err := extractIDs(t.current, primary, fallback)
	if err != nil {
		return nil, nil, err
	}
	in := func(list []drf.Value, v drf.Value) bool {
		return slices.ContainsFunc(list, func(x drf.Value) bool { return x.Str() == v.Str() })
	}
	for _, v := range req {
		if !in(cur, v) {
			added = append(added, v)
		}
	}
	for _, v := range cur {
		if !in(req, v) {
			dropped = append(dropped, v)
		}
	}
	return added, dropped, nil
}

// getByPK is Model.objects.get(pk=s) for an already-validated UUID string.
func (t *activityTask) getByPK(ctx context.Context, sql string, s string, dst ...any) error {
	id, ok := drf.ParseUUID(s)
	if !ok {
		return abort("get(pk=%q)", s)
	}
	err := t.a.db.QueryRow(ctx, sql, id).Scan(dst...)
	if errors.Is(err, pgx.ErrNoRows) {
		return abort("%s DoesNotExist", s)
	}
	return err
}

func (t *activityTask) trackLabels(ctx context.Context) error {
	added, dropped, err := t.diffIDs("label_ids", "labels")
	if err != nil {
		return err
	}
	const q = `SELECT id, name FROM labels WHERE id = $1 AND deleted_at IS NULL`
	for _, v := range added {
		if ok, err := isValidUUID4(v); err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		var (
			id   uuid.UUID
			name string
		)
		if err := t.getByPK(ctx, q, v.Str(), &id, &name); err != nil {
			return err
		}
		t.add(&activity{Verb: "updated", Field: strp("labels"), Comment: "added label ", OldValue: strp(""),
			NewValue: &name, NewIdentifier: &id})
	}
	for _, v := range dropped {
		if ok, err := isValidUUID4(v); err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		var (
			id   uuid.UUID
			name string
		)
		if err := t.getByPK(ctx, q, v.Str(), &id, &name); err != nil {
			return err
		}
		t.add(&activity{Verb: "updated", OldValue: &name, NewValue: strp(""), Field: strp("labels"),
			Comment: "removed label ", OldIdentifier: &id})
	}
	return nil
}

func (t *activityTask) trackAssignees(ctx context.Context) error {
	added, dropped, err := t.diffIDs("assignee_ids", "assignees")
	if err != nil {
		return err
	}
	const q = `SELECT id, display_name FROM users WHERE id = $1`
	var subscribers []uuid.UUID
	for _, v := range added {
		if ok, err := isValidUUID4(v); err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		var (
			id   uuid.UUID
			name string
		)
		if err := t.getByPK(ctx, q, v.Str(), &id, &name); err != nil {
			return err
		}
		t.add(&activity{Verb: "updated", OldValue: strp(""), NewValue: &name, Field: strp("assignees"),
			Comment: "added assignee ", NewIdentifier: &id})
		subscribers = append(subscribers, id)
	}
	if len(subscribers) > 0 {
		if _, err := t.a.db.Exec(ctx, `INSERT INTO issue_subscribers (subscriber_id, issue_id, workspace_id, project_id,
				created_by_id, updated_by_id, created_at, updated_at)
			SELECT x, $2, $3, $4, x, x, clock_timestamp(), clock_timestamp() FROM unnest($1::uuid[]) AS x
			ON CONFLICT DO NOTHING`, subscribers, *t.issueID, t.workspaceID, t.projectID); err != nil {
			return err
		}
	}
	for _, v := range dropped {
		if ok, err := isValidUUID4(v); err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		var (
			id   uuid.UUID
			name string
		)
		if err := t.getByPK(ctx, q, v.Str(), &id, &name); err != nil {
			return err
		}
		t.add(&activity{Verb: "updated", OldValue: &name, NewValue: strp(""), Field: strp("assignees"),
			Comment: "removed assignee ", OldIdentifier: &id})
	}
	return nil
}

func (t *activityTask) trackEstimatePoint(ctx context.Context) error {
	cv, cok := t.current.get("estimate_point")
	rv, rok := t.requested.get("estimate_point")
	if pyEqual(cv, cok, rv, rok) {
		return nil
	}
	point := func(v drf.Value, ok bool) (*string, *string, error) {
		if !ok || v.IsNull() {
			return nil, nil, nil
		}
		id, valid := drf.ParseUUID(drf.PyStr(v))
		if !valid {
			return nil, nil, abort("EstimatePoint pk %s", drf.PyRepr(v))
		}
		var value, typ string
		err := t.a.db.QueryRow(ctx, `SELECT ep.value, e.type FROM estimate_points ep JOIN estimates e ON e.id = ep.estimate_id
			WHERE ep.id = $1 AND ep.deleted_at IS NULL`, id).Scan(&value, &typ)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return &value, &typ, err
	}
	oldValue, _, err := point(cv, cok)
	if err != nil {
		return err
	}
	newValue, newType, err := point(rv, rok)
	if err != nil {
		return err
	}
	if newType == nil {
		// "estimate_" + new_estimate.estimate.type on None.
		return abort("estimate point removed")
	}
	ident := func(v drf.Value, ok bool) *uuid.UUID {
		if !ok || v.IsNull() {
			return nil
		}
		id, _ := drf.ParseUUID(drf.PyStr(v))
		return &id
	}
	t.add(&activity{Verb: "updated", OldIdentifier: ident(cv, cok), NewIdentifier: ident(rv, rok),
		OldValue: oldValue, NewValue: newValue, Field: strp("estimate_" + *newType),
		Comment: "updated the estimate point to "})
	return nil
}

func (t *activityTask) trackArchivedAt(context.Context) error {
	cv, cok := t.current.get("archived_at")
	rv, rok := t.requested.get("archived_at")
	if pyEqual(cv, cok, rv, rok) {
		return nil
	}
	if !rok || rv.IsNull() {
		t.add(&activity{Comment: "has restored the issue", Verb: "updated", Field: strp("archived_at"),
			OldValue: strp("archive"), NewValue: strp("restore")})
		return nil
	}
	comment, value := "Actor has archived the issue", "manual_archive"
	if auto, ok := t.requested.get("automation"); ok && drf.PyTruthy(auto) {
		comment, value = "Plane has archived the issue", "archive"
	}
	t.add(&activity{Comment: comment, Verb: "updated", Field: strp("archived_at"), NewValue: &value})
	return nil
}

func (t *activityTask) trackClosedTo(ctx context.Context) error {
	v, ok := t.requested.get("closed_to")
	if !ok || v.IsNull() {
		return nil
	}
	id, valid := drf.ParseUUID(drf.PyStr(v))
	if !valid {
		return abort("State pk %s", drf.PyRepr(v))
	}
	var name string
	err := t.a.db.QueryRow(ctx, `SELECT name FROM states WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL
		AND "group" <> 'triage'`, id, t.projectID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return abort("State DoesNotExist")
	}
	if err != nil {
		return err
	}
	t.add(&activity{Verb: "updated", NewValue: &name, Field: strp("state"), Comment: "Plane updated the state to ",
		NewIdentifier: &id})
	return nil
}

// mentionsOf is extract_mentions on a task's requested_data or
// current_instance: [] for anything that is not JSON text of a dict with a
// description_html string.
func mentionsOf(s *string, isDict bool) []string {
	if s == nil || isDict {
		return nil
	}
	p := loadDict(s)
	if !p.d.IsDict() {
		return nil
	}
	v, ok := p.get("description_html")
	if !ok || !v.IsString() {
		return nil
	}
	ids, ok := sanitize.Mentions(v.Str())
	if !ok {
		return nil
	}
	return dedupe(ids)
}

func dedupe(ss []string) []string {
	var out []string
	for _, s := range ss {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func minus(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

func parseUUIDs(ss []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		id, ok := drf.ParseUUID(s)
		if !ok {
			return nil, abort("“%s” is not a valid UUID", s)
		}
		out = append(out, id)
	}
	return out, nil
}

type notificationPref struct {
	propertyChange, stateChange, comment, mention, issueCompleted bool
}

func (a *API) notificationPref(ctx context.Context, user uuid.UUID) (*notificationPref, error) {
	rows, err := a.db.Query(ctx, `SELECT property_change, state_change, comment, mention, issue_completed
		FROM user_notification_preferences WHERE user_id = $1 AND deleted_at IS NULL LIMIT 2`, user)
	if err != nil {
		return nil, err
	}
	prefs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (notificationPref, error) {
		var p notificationPref
		err := row.Scan(&p.propertyChange, &p.stateChange, &p.comment, &p.mention, &p.issueCompleted)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	if len(prefs) != 1 {
		return nil, abort("UserNotificationPreference.get(user_id=%s): %d rows", user, len(prefs))
	}
	return &prefs[0], nil
}

// notifIssue is the issue as the notifications task reads it.
type notifIssue struct {
	id                    uuid.UUID
	name                  string
	sequenceID            int
	createdBy             *uuid.UUID
	stateName, stateGroup *string
	identifier            string
	projectID             uuid.UUID
	slug                  string
}

func (i *notifIssue) data(full bool) (map[string]any, error) {
	if i.stateName == nil {
		return nil, abort("issue.state is None")
	}
	m := map[string]any{
		"id": i.id.String(), "name": i.name, "identifier": i.identifier, "sequence_id": i.sequenceID,
		"state_name": *i.stateName, "state_group": *i.stateGroup,
	}
	if full {
		m["project_id"] = i.projectID.String()
		m["workspace_slug"] = i.slug
	}
	return m, nil
}

// pyStrOrNone is str(x), "None" for None.
func pyStrOrNone(s *string) string {
	if s == nil {
		return "None"
	}
	return *s
}

func identStr(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// activityData is the "issue_activity" block built from an
// IssueActivitySerializer dict: it has no "actor_id" key, so "actor" is
// always "None".
func activityData(act *activity) map[string]any {
	return map[string]any{
		"id": act.ID.String(), "verb": act.Verb, "field": pyStrOrNone(act.Field), "actor": "None",
		"new_value": pyStrOrNone(act.NewValue), "old_value": pyStrOrNone(act.OldValue),
		"old_identifier": identStr(act.OldIdentifier), "new_identifier": identStr(act.NewIdentifier),
	}
}

type notificationRow struct {
	receiver uuid.UUID
	sender   string
	title    string
	message  *string
	data     map[string]any
}

type emailLogRow struct {
	// receiver is the receiver_id the task passes: a user id, or the
	// stale boolean `subscriber` argument (which fails the FK).
	receiver any
	data     map[string]any
}

// notifications ports bgtasks.notification_task.notifications, statement
// for statement where it matters: an exception ends it with everything
// written so far kept.
func (a *API) notifications(ctx context.Context, j issueActivityJob, created []*activity) error {
	switch j.Type {
	case "cycle.activity.created", "cycle.activity.deleted", "module.activity.created", "module.activity.deleted",
		"issue_reaction.activity.created", "issue_reaction.activity.deleted", "comment_reaction.activity.created",
		"comment_reaction.activity.deleted", "issue_vote.activity.created", "issue_vote.activity.deleted",
		"issue_draft.activity.created", "issue_draft.activity.updated", "issue_draft.activity.deleted":
		return nil
	}
	projectID, _ := uuid.Parse(j.ProjectID)
	actor, _ := uuid.Parse(j.ActorID)
	issueID, _ := uuid.Parse(j.IssueID)
	loc := time.UTC
	if j.ActorTZ != "" {
		if l, err := time.LoadLocation(j.ActorTZ); err == nil {
			loc = l
		}
	}

	rows, err := a.db.Query(ctx, `SELECT member_id FROM project_members WHERE project_id = $1 AND is_active
		AND deleted_at IS NULL`, projectID)
	if err != nil {
		return err
	}
	members, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	memberStr := make([]string, len(members))
	for i, m := range members {
		memberStr[i] = m.String()
	}

	newer := mentionsOf(j.RequestedData, false)
	older := mentionsOf(j.CurrentInstance, j.CurrentIsDict)
	var newMentions []string
	for _, m := range minus(newer, older) {
		if slices.Contains(memberStr, m) {
			newMentions = append(newMentions, m)
		}
	}
	removed := minus(older, newer)

	mentionSubs, err := a.mentionSubscribers(ctx, projectID, issueID, newer)
	if err != nil {
		return err
	}
	var commentMentions, allCommentMentions []string
	for _, act := range created {
		if act.IssueCommentID == nil {
			continue
		}
		newValue := pyStrOrNone(act.NewValue)
		mentioned, _ := sanitize.Mentions(newValue)
		allCommentMentions = append(allCommentMentions, dedupe(mentioned)...)
		fresh := dedupe(mentioned)
		if act.OldValue != nil {
			was, _ := sanitize.Mentions(*act.OldValue)
			fresh = minus(fresh, dedupe(was))
		}
		commentMentions = append(commentMentions, fresh...)
		var kept []string
		for _, m := range commentMentions {
			id, ok := drf.ParseUUID(m)
			if !ok {
				return abort("UUID(%q)", m)
			}
			if slices.Contains(members, id) {
				kept = append(kept, m)
			}
		}
		commentMentions = kept
	}
	commentSubs, err := a.mentionSubscribers(ctx, projectID, issueID, allCommentMentions)
	if err != nil {
		return err
	}

	exclude, err := parseUUIDs(append(append(slices.Clone(newMentions), commentMentions...), j.ActorID))
	if err != nil {
		return err
	}
	rows, err = a.db.Query(ctx, `SELECT subscriber_id FROM issue_subscribers WHERE project_id = $1 AND issue_id = $2
		AND deleted_at IS NULL AND subscriber_id = ANY($3) AND NOT subscriber_id = ANY($4) ORDER BY created_at DESC`,
		projectID, issueID, members, exclude)
	if err != nil {
		return err
	}
	subscribers, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}

	var issue *notifIssue
	{
		var i notifIssue
		err := a.db.QueryRow(ctx, `SELECT i.id, i.name, i.sequence_id, i.created_by_id, s.name, s."group", p.identifier,
				p.id, w.slug
			FROM issues i JOIN projects p ON p.id = i.project_id JOIN workspaces w ON w.id = p.workspace_id
			LEFT JOIN states s ON s.id = i.state_id
			WHERE i.id = $1 AND i.deleted_at IS NULL`, issueID).
			Scan(&i.id, &i.name, &i.sequenceID, &i.createdBy, &i.stateName, &i.stateGroup, &i.identifier, &i.projectID, &i.slug)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			issue = &i
		}
	}

	if j.Subscriber {
		// get_or_create inside try/except: failures are ignored.
		_, _ = a.db.Exec(ctx, `INSERT INTO issue_subscribers (project_id, issue_id, subscriber_id, workspace_id, created_by_id)
			SELECT p.id, $2, $3, p.workspace_id, $3 FROM projects p WHERE p.id = $1
				AND NOT EXISTS (SELECT 1 FROM issue_subscribers WHERE project_id = $1 AND issue_id = $2
					AND subscriber_id = $3 AND deleted_at IS NULL)`, projectID, issueID, actor)
	}
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT workspace_id FROM projects WHERE id = $1 AND deleted_at IS NULL`, projectID).
		Scan(&workspaceID); err != nil {
		return err
	}
	rows, err = a.db.Query(ctx, `SELECT assignee_id FROM issue_assignees WHERE issue_id = $1 AND project_id = $2
		AND deleted_at IS NULL AND assignee_id = ANY($3)`, issueID, projectID, members)
	if err != nil {
		return err
	}
	assignees, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	subscribers = dedupeUUIDs(subscribers)
	subscribers = slices.DeleteFunc(subscribers, func(u uuid.UUID) bool { return u == actor })

	var (
		notifs []notificationRow
		emails []emailLogRow
	)
	// subscriberVar is the task's `subscriber`, which the loop below
	// rebinds and later code still reads.
	var subscriberVar any = j.Subscriber
	for _, sub := range subscribers {
		subscriberVar = sub
		if issue == nil {
			return abort("issue is None")
		}
		var sender string
		switch {
		case issue.createdBy != nil && *issue.createdBy == sub:
			sender = "in_app:issue_activities:created"
		case slices.Contains(assignees, sub) && (issue.createdBy == nil || !slices.Contains(assignees, *issue.createdBy)):
			sender = "in_app:issue_activities:assigned"
		default:
			sender = "in_app:issue_activities:subscribed"
		}
		pref, err := a.notificationPref(ctx, sub)
		if err != nil {
			return err
		}
		for _, act := range created {
			if act.IssueID == nil {
				return abort("issue_detail is None")
			}
			if *act.IssueID != issueID {
				continue
			}
			field := pyStrOrNone(act.Field)
			if act.Field != nil && *act.Field == "description" {
				continue
			}
			var sendEmail bool
			switch {
			case field == "state" && pref.stateChange:
				sendEmail = true
			case field == "state" && pref.issueCompleted && act.NewIdentifier != nil && a.stateCompleted(ctx, projectID, *act.NewIdentifier):
				sendEmail = true
			case field == "comment" && pref.comment:
				sendEmail = true
			case pref.propertyChange:
				sendEmail = true
			}
			issueComment := ""
			if act.IssueCommentID != nil {
				var stripped *string
				err := a.db.QueryRow(ctx, `SELECT comment_stripped FROM issue_comments WHERE id = $1 AND issue_id = $2
					AND project_id = $3 AND workspace_id = $4 AND deleted_at IS NULL`,
					*act.IssueCommentID, issueID, projectID, workspaceID).Scan(&stripped)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if err == nil {
					issueComment = pyStrOrNone(stripped)
				}
			}
			issueData, err := issue.data(false)
			if err != nil {
				return err
			}
			ad := activityData(act)
			ad["issue_comment"] = issueComment
			notifs = append(notifs, notificationRow{receiver: sub, sender: sender, title: act.Comment,
				data: map[string]any{"issue": issueData, "issue_activity": ad}})
			if sendEmail {
				issueData, _ := issue.data(true)
				ad := activityData(act)
				ad["issue_comment"] = issueComment
				ad["activity_time"] = httpx.FormatDateTime(act.CreatedAt, loc)
				emails = append(emails, emailLogRow{receiver: sub, data: map[string]any{"issue": issueData, "issue_activity": ad}})
			}
		}
	}

	if subs := append(mentionSubs, commentSubs...); len(subs) > 0 {
		if _, err := a.db.Exec(ctx, `INSERT INTO issue_subscribers (workspace_id, project_id, issue_id, subscriber_id,
				created_at, updated_at)
			SELECT $1, $2, $3, x, clock_timestamp(), clock_timestamp() FROM unnest($4::uuid[]) AS x
			ON CONFLICT DO NOTHING`, workspaceID, projectID, issueID, subs); err != nil {
			return err
		}
	}

	var last *activity
	{
		var l activity
		err := a.db.QueryRow(ctx, `SELECT id, verb, field, actor_id, new_value, old_value, created_at FROM issue_activities
			WHERE issue_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1`, issueID).
			Scan(&l.ID, &l.Verb, &l.Field, &l.ActorID, &l.NewValue, &l.OldValue, &l.CreatedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			last = &l
		}
	}
	var actorName string
	if err := a.db.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, actor).Scan(&actorName); err != nil {
		return err
	}
	// issue_activity is the loop variable of the loops over created: its
	// last element, or unbound when there is none.
	var lastCreated *activity
	if len(created) > 0 {
		lastCreated = created[len(created)-1]
	}
	mentionData := func(act *activity) (map[string]any, error) {
		issueData, err := issue.data(false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"issue": issueData, "issue_activity": activityData(act)}, nil
	}
	mentionEmail := func(receiver any, act *activity, activityTime string) error {
		data, err := mentionData(act)
		if err != nil {
			return err
		}
		data["issue_activity"].(map[string]any)["field"] = "mention"
		data["issue_activity"].(map[string]any)["activity_time"] = activityTime
		emails = append(emails, emailLogRow{receiver: receiver, data: data})
		return nil
	}

	for _, m := range commentMentions {
		if m == j.ActorID {
			continue
		}
		mid, _ := drf.ParseUUID(m)
		pref, err := a.notificationPref(ctx, mid)
		if err != nil {
			return err
		}
		for _, act := range created {
			if issue == nil {
				return abort("issue is None")
			}
			data, err := mentionData(act)
			if err != nil {
				return err
			}
			msg := actorName + " has mentioned you in a comment in issue " + issue.name
			if pref.mention {
				full, _ := issue.data(true)
				ad := activityData(act)
				ad["field"] = "mention"
				ad["activity_time"] = httpx.FormatDateTime(act.CreatedAt, loc)
				emails = append(emails, emailLogRow{receiver: mid, data: map[string]any{"issue": full, "issue_activity": ad}})
			}
			notifs = append(notifs, notificationRow{receiver: mid, sender: "in_app:issue_activities:mentioned",
				message: &msg, data: data})
		}
	}

	for _, m := range newMentions {
		if m == j.ActorID {
			continue
		}
		mid, _ := drf.ParseUUID(m)
		pref, err := a.notificationPref(ctx, mid)
		if err != nil {
			return err
		}
		if issue == nil {
			return abort("issue is None")
		}
		msg := "You have been mentioned in the issue " + issue.name
		if last != nil && last.Field != nil && *last.Field == "description" && last.ActorID != nil && *last.ActorID == actor {
			if lastCreated == nil {
				return abort("NameError: issue_activity")
			}
			full, err := issue.data(true)
			if err != nil {
				return err
			}
			ad := map[string]any{
				"id": last.ID.String(), "verb": last.Verb, "field": pyStrOrNone(last.Field), "actor": last.ActorID.String(),
				"new_value": pyStrOrNone(last.NewValue), "old_value": pyStrOrNone(last.OldValue),
				"old_identifier": identStr(lastCreated.OldIdentifier), "new_identifier": identStr(lastCreated.NewIdentifier),
			}
			notifs = append(notifs, notificationRow{receiver: mid, sender: "in_app:issue_activities:mentioned",
				message: &msg, data: map[string]any{"issue": full, "issue_activity": ad}})
			if pref.mention {
				short, _ := issue.data(false)
				ead := map[string]any{}
				for k, v := range ad {
					ead[k] = v
				}
				ead["field"] = "mention"
				ead["activity_time"] = pyDateTimeStr(last.CreatedAt.UTC())
				emails = append(emails, emailLogRow{receiver: subscriberVar, data: map[string]any{"issue": short, "issue_activity": ead}})
			}
			continue
		}
		for _, act := range created {
			data, err := mentionData(act)
			if err != nil {
				return err
			}
			if pref.mention {
				if err := mentionEmail(subscriberVar, act, httpx.FormatDateTime(act.CreatedAt, loc)); err != nil {
					return err
				}
			}
			notifs = append(notifs, notificationRow{receiver: mid, sender: "in_app:issue_activities:mentioned",
				message: &msg, data: data})
		}
	}

	// update_mentions_for_issue
	if len(newMentions) > 0 {
		if issue == nil {
			return abort("IssueMention(issue=None)")
		}
		ids, _ := parseUUIDs(newMentions)
		_, err := a.db.Exec(ctx, `INSERT INTO issue_mentions (mention_id, issue_id, project_id, workspace_id, created_at, updated_at)
			SELECT x, $2, $3, $4, clock_timestamp(), clock_timestamp() FROM unnest($1::uuid[]) AS x`,
			ids, issueID, projectID, workspaceID)
		if err != nil {
			return err
		}
	}
	if len(removed) > 0 && issue != nil {
		ids, err := parseUUIDs(removed)
		if err != nil {
			return err
		}
		if _, err := a.db.Exec(ctx, `UPDATE issue_mentions SET deleted_at = now() WHERE issue_id = $1
			AND mention_id = ANY($2) AND deleted_at IS NULL`, issueID, ids); err != nil {
			return err
		}
	}

	if len(notifs) > 0 {
		batch := &pgx.Batch{}
		for _, n := range notifs {
			data, err := json.Marshal(n.data)
			if err != nil {
				return err
			}
			var message any
			if n.message != nil {
				raw, _ := json.Marshal(*n.message)
				message = string(raw)
			}
			batch.Queue(`INSERT INTO notifications (workspace_id, project_id, sender, triggered_by_id, receiver_id,
					entity_identifier, entity_name, title, message, data, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, 'issue', $7, $8::jsonb, $9::jsonb, clock_timestamp(), clock_timestamp())`,
				workspaceID, projectID, n.sender, actor, n.receiver, issueID, n.title, message, string(data))
		}
		if err := pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error { return tx.SendBatch(ctx, batch).Close() }); err != nil {
			return err
		}
	}
	if len(emails) > 0 {
		batch := &pgx.Batch{}
		for _, e := range emails {
			receiver, ok := e.receiver.(uuid.UUID)
			if !ok {
				// receiver_id=True/False: UUID(int=1)/UUID(int=0) fails its
				// foreign key and rolls the whole bulk_create back.
				return abort("email log receiver %v", e.receiver)
			}
			data, err := json.Marshal(e.data)
			if err != nil {
				return err
			}
			batch.Queue(`INSERT INTO email_notification_logs (triggered_by_id, receiver_id, entity_identifier, entity_name,
					data, created_at, updated_at)
				VALUES ($1, $2, $3, 'issue', $4::jsonb, clock_timestamp(), clock_timestamp()) ON CONFLICT DO NOTHING`,
				actor, receiver, issueID, string(data))
		}
		if err := pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error { return tx.SendBatch(ctx, batch).Close() }); err != nil {
			return err
		}
	}
	return nil
}

func dedupeUUIDs(ids []uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (a *API) stateCompleted(ctx context.Context, projectID, stateID uuid.UUID) bool {
	var ok bool
	_ = a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM states WHERE project_id = $1 AND id = $2 AND "group" = 'completed'
		AND deleted_at IS NULL)`, projectID, stateID).Scan(&ok)
	return ok
}

// mentionSubscribers is extract_mentions_as_subscribers: mentioned active
// project members not yet subscribed, assigned or the issue's creator.
func (a *API) mentionSubscribers(ctx context.Context, projectID, issueID uuid.UUID, mentions []string) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for _, m := range mentions {
		id, ok := drf.ParseUUID(m)
		if !ok {
			return nil, abort("“%s” is not a valid UUID", m)
		}
		var add bool
		if err := a.db.QueryRow(ctx, `SELECT
				NOT EXISTS (SELECT 1 FROM issue_subscribers WHERE issue_id = $1 AND subscriber_id = $2 AND project_id = $3
					AND deleted_at IS NULL)
				AND NOT EXISTS (SELECT 1 FROM issue_assignees WHERE project_id = $3 AND issue_id = $1 AND assignee_id = $2
					AND deleted_at IS NULL)
				AND NOT EXISTS (SELECT 1 FROM issues WHERE project_id = $3 AND id = $1 AND created_by_id = $2
					AND deleted_at IS NULL)
				AND EXISTS (SELECT 1 FROM project_members WHERE project_id = $3 AND member_id = $2 AND is_active
					AND deleted_at IS NULL)`, issueID, id, projectID).Scan(&add); err != nil {
			return nil, err
		}
		if add {
			out = append(out, id)
		}
	}
	return out, nil
}

// descriptionVersionJob is issue_description_version_task's arguments.
type descriptionVersionJob struct {
	UpdatedIssue string `json:"updated_issue"`
	IssueID      string `json:"issue_id"`
	UserID       string `json:"user_id"`
	IsCreating   bool   `json:"is_creating"`
}

func (descriptionVersionJob) Kind() string { return "issue_description_version" }

func (a *API) enqueueDescriptionVersion(ctx context.Context, j descriptionVersionJob) {
	if err := jobs.Enqueue(ctx, a.jobs, j); err != nil {
		a.log.Error("enqueue issue_description_version_task", "err", err)
	}
}

// issueDescriptionVersion ports issue_description_version_task: start a
// new version, or fold the edit into the user's latest one from the last
// ten minutes.
func (a *API) issueDescriptionVersion(ctx context.Context, j descriptionVersionJob) error {
	issueID, err := uuid.Parse(j.IssueID)
	if err != nil {
		return err
	}
	user, err := uuid.Parse(j.UserID)
	if err != nil {
		return err
	}
	var (
		html                   string
		projectID, workspaceID uuid.UUID
		createdBy, updatedBy   *uuid.UUID
	)
	err = a.db.QueryRow(ctx, `SELECT description_html, project_id, workspace_id, created_by_id, updated_by_id
		FROM issues WHERE id = $1 AND deleted_at IS NULL`, issueID).Scan(&html, &projectID, &workspaceID, &createdBy, &updatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !j.IsCreating {
		cur := loadDict(&j.UpdatedIssue)
		if v, ok := cur.get("description_html"); ok && v.IsString() && v.Str() == html {
			return nil
		}
	}
	return pgx.BeginFunc(ctx, a.db, func(tx pgx.Tx) error {
		var (
			id          uuid.UUID
			owner       uuid.UUID
			savedRecent bool
		)
		err := tx.QueryRow(ctx, `SELECT id, owned_by_id, now() - last_saved_at <= interval '600 seconds'
			FROM issue_description_versions WHERE issue_id = $1 AND deleted_at IS NULL
			ORDER BY last_saved_at DESC LIMIT 1`, issueID).Scan(&id, &owner, &savedRecent)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && owner == user && savedRecent {
			_, err := tx.Exec(ctx, `UPDATE issue_description_versions v SET description_json = i.description_json,
					description_html = i.description_html, description_binary = i.description_binary,
					description_stripped = i.description_stripped, last_saved_at = clock_timestamp()
				FROM issues i WHERE v.id = $1 AND i.id = $2`, id, issueID)
			return err
		}
		// log_issue_description_version: created_by is the acting user (the
		// model's save() overrides the passed one).
		_, err = tx.Exec(ctx, `INSERT INTO issue_description_versions (workspace_id, project_id, created_by_id, owned_by_id,
				last_saved_at, issue_id, description_binary, description_html, description_stripped, description_json,
				created_at, updated_at)
			SELECT workspace_id, project_id, $2, $2, statement_timestamp(), id, description_binary, description_html,
				description_stripped, description_json, clock_timestamp(), clock_timestamp()
			FROM issues WHERE id = $1`, issueID, user)
		return err
	})
}
