package api

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
)

// The issue_activity trackers for comments and reactions
// (bgtasks.issue_activities_task: create/update/delete_comment_activity,
// create/delete_issue_reaction_activity, create/delete_comment_reaction_activity).
// issueActivity dispatches here through discussionActivity.

// discussionTypes are the activity types discussionActivity handles.
var discussionTypes = map[string]func(*activityTask, context.Context) error{
	"comment.activity.created":          (*activityTask).createCommentActivity,
	"comment.activity.updated":          (*activityTask).updateCommentActivity,
	"comment.activity.deleted":          (*activityTask).deleteCommentActivity,
	"issue_reaction.activity.created":   (*activityTask).createIssueReactionActivity,
	"issue_reaction.activity.deleted":   (*activityTask).deleteIssueReactionActivity,
	"comment_reaction.activity.created": (*activityTask).createCommentReactionActivity,
	"comment_reaction.activity.deleted": (*activityTask).deleteCommentReactionActivity,
}

// discussionActivity runs the tracker for a comment or reaction event.
func (t *activityTask) discussionActivity(ctx context.Context) error {
	fn, ok := discussionTypes[t.j.Type]
	if !ok {
		return nil
	}
	t.requested = loadDict(t.j.RequestedData)
	t.current = loadDict(t.j.CurrentInstance)
	return fn(t, ctx)
}

// dictGet is dict.get(key, default) as a TextField stores it: str() of the
// value, nil for None, def for a missing key. A json.loads of None (no
// dict at all) raises AttributeError.
func dictGet(p *pyDict, key string, def *string) (*string, error) {
	if p == nil {
		return nil, abort("None.get(%q)", key)
	}
	v, ok := p.get(key)
	if !ok {
		return def, nil
	}
	if v.IsNull() {
		return nil, nil
	}
	return strp(drf.PyStr(v)), nil
}

// dictUUID is dict.get(key) assigned to a UUID column: nil for None.
func dictUUID(p *pyDict, key string) (*uuid.UUID, error) {
	s, err := dictGet(p, key, nil)
	if err != nil || s == nil {
		return nil, err
	}
	id, ok := drf.ParseUUID(*s)
	if !ok {
		return nil, abort("“%s” is not a valid UUID", *s)
	}
	return &id, nil
}

// create_comment_activity
func (t *activityTask) createCommentActivity(context.Context) error {
	newValue, err := dictGet(t.requested, "comment_html", strp(""))
	if err != nil {
		return err
	}
	id, err := dictUUID(t.requested, "id")
	if err != nil {
		return err
	}
	t.add(&activity{Comment: "created a comment", Verb: "created", Field: strp("comment"), NewValue: newValue,
		NewIdentifier: id, IssueCommentID: id})
	return nil
}

// update_comment_activity: an activity whenever comment_html differs from
// the request's (a request without one included).
func (t *activityTask) updateCommentActivity(context.Context) error {
	if t.current == nil || t.requested == nil {
		return abort("None.get")
	}
	cv, cok := t.current.get("comment_html")
	rv, rok := t.requested.get("comment_html")
	if pyEqual(cv, cok, rv, rok) {
		return nil
	}
	oldValue, err := dictGet(t.current, "comment_html", strp(""))
	if err != nil {
		return err
	}
	newValue, err := dictGet(t.requested, "comment_html", strp(""))
	if err != nil {
		return err
	}
	id, err := dictUUID(t.current, "id")
	if err != nil {
		return err
	}
	t.add(&activity{Comment: "updated a comment", Verb: "updated", Field: strp("comment"), OldValue: oldValue,
		OldIdentifier: id, NewValue: newValue, NewIdentifier: id, IssueCommentID: id})
	return nil
}

// delete_comment_activity
func (t *activityTask) deleteCommentActivity(context.Context) error {
	id, err := dictUUID(t.requested, "comment_id")
	if err != nil {
		return err
	}
	t.add(&activity{IssueCommentID: id, Comment: "deleted the comment", Verb: "deleted", Field: strp("comment")})
	return nil
}

// reactionOf is data.get("reaction") when data (requested_data or
// current_instance) is a non-empty dict; nil when the tracker does nothing.
func reactionOf(p *pyDict) *string {
	if p == nil || !p.d.IsDict() || len(p.d.Keys()) == 0 {
		return nil
	}
	v, ok := p.get("reaction")
	if !ok || v.IsNull() {
		return nil
	}
	return strp(drf.PyStr(v))
}

// create_issue_reaction_activity: the newest live reaction with this code
// by the actor anywhere in the project.
func (t *activityTask) createIssueReactionActivity(ctx context.Context) error {
	reaction := reactionOf(t.requested)
	if reaction == nil {
		return nil
	}
	var id uuid.UUID
	err := t.a.db.QueryRow(ctx, `SELECT id FROM issue_reactions WHERE reaction = $1 AND project_id = $2 AND actor_id = $3
		AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1`, *reaction, t.projectID, t.actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	t.add(&activity{Verb: "created", NewValue: reaction, Field: strp("reaction"), Comment: "added the reaction",
		NewIdentifier: &id})
	return nil
}

// delete_issue_reaction_activity
func (t *activityTask) deleteIssueReactionActivity(context.Context) error {
	reaction := reactionOf(t.current)
	if reaction == nil {
		return nil
	}
	id, err := dictUUID(t.current, "identifier")
	if err != nil {
		return err
	}
	t.add(&activity{Verb: "deleted", OldValue: reaction, Field: strp("reaction"), Comment: "removed the reaction",
		OldIdentifier: id})
	return nil
}

// addOnIssue records an activity on another issue than the task's (the
// comment reaction trackers take it from the comment).
func (t *activityTask) addOnIssue(issue uuid.UUID, act *activity) {
	act.IssueID = &issue
	act.ActorID = &t.actor
	t.activities = append(t.activities, act)
}

// create_comment_reaction_activity: the newest live reaction with this code
// by the actor in the project, on a comment that must still be live.
func (t *activityTask) createCommentReactionActivity(ctx context.Context) error {
	reaction := reactionOf(t.requested)
	if reaction == nil {
		return nil
	}
	var id, commentID uuid.UUID
	err := t.a.db.QueryRow(ctx, `SELECT r.id, c.id FROM comment_reactions r JOIN issue_comments c ON c.id = r.comment_id
		WHERE r.reaction = $1 AND r.project_id = $2 AND r.actor_id = $3 AND r.deleted_at IS NULL
		ORDER BY r.created_at DESC LIMIT 1`, *reaction, t.projectID, t.actor).Scan(&id, &commentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return abort("cannot unpack None")
	}
	if err != nil {
		return err
	}
	var issue uuid.UUID
	err = t.a.db.QueryRow(ctx, `SELECT issue_id FROM issue_comments WHERE id = $1 AND project_id = $2
		AND deleted_at IS NULL`, commentID, t.projectID).Scan(&issue)
	if errors.Is(err, pgx.ErrNoRows) {
		return abort("IssueComment.DoesNotExist")
	}
	if err != nil {
		return err
	}
	t.addOnIssue(issue, &activity{Verb: "created", NewValue: reaction, Field: strp("reaction"),
		Comment: "added the reaction", NewIdentifier: &id})
	return nil
}

// delete_comment_reaction_activity: nothing once the comment is gone.
func (t *activityTask) deleteCommentReactionActivity(ctx context.Context) error {
	reaction := reactionOf(t.current)
	if reaction == nil {
		return nil
	}
	commentID, err := dictUUID(t.current, "comment_id")
	if err != nil {
		return err
	}
	var issue uuid.UUID
	err = t.a.db.QueryRow(ctx, `SELECT issue_id FROM issue_comments WHERE id = $1 AND project_id = $2
		AND deleted_at IS NULL`, commentID, t.projectID).Scan(&issue)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	id, err := dictUUID(t.current, "identifier")
	if err != nil {
		return err
	}
	t.addOnIssue(issue, &activity{Verb: "deleted", OldValue: reaction, Field: strp("reaction"),
		Comment: "removed the reaction", OldIdentifier: id})
	return nil
}
