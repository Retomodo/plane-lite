package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/mail"
)

// stackEmailNotificationJob is bgtasks.email_notification_task.stack_email_notification:
// it groups the unprocessed email_notification_logs by receiver and issue,
// queues one send_email_notification per pair, then marks them processed.
type stackEmailNotificationJob struct{}

func (stackEmailNotificationJob) Kind() string { return "stack_email_notification" }

// digestActorChanges is one actor's entry of the task's notification_data
// ({actor_id: [log data, ...]}), kept as a list to keep its order.
type digestActorChanges struct {
	Actor   string            `json:"actor"`
	Changes []json.RawMessage `json:"changes"`
}

// sendEmailNotificationJob is email_notification_task.send_email_notification.
// IssueID is nil when the logs' entity_identifier is.
type sendEmailNotificationJob struct {
	IssueID              *string              `json:"issue_id"`
	NotificationData     []digestActorChanges `json:"notification_data"`
	ReceiverID           string               `json:"receiver_id"`
	EmailNotificationIDs []string             `json:"email_notification_ids"`
}

func (sendEmailNotificationJob) Kind() string                 { return "send_email_notification" }
func (sendEmailNotificationJob) InsertOpts() river.InsertOpts { return emailInsertOpts }

func (a *API) runStackEmailNotification(ctx context.Context, _ stackEmailNotificationJob) error {
	// order_by("receiver") follows User's ordering (-created_at).
	rows, err := a.db.Query(ctx, `SELECT l.id::text, l.receiver_id::text, l.triggered_by_id::text,
			l.entity_identifier::text, coalesce(l.data, 'null'::jsonb)
		FROM email_notification_logs l JOIN users u ON l.receiver_id = u.id
		WHERE l.deleted_at IS NULL AND l.processed_at IS NULL ORDER BY u.created_at DESC`)
	if err != nil {
		return err
	}
	type logRow struct {
		id, receiver string
		actor        *string
		issue        *string
		data         json.RawMessage
	}
	logs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (logRow, error) {
		var l logRow
		err := r.Scan(&l.id, &l.receiver, &l.actor, &l.issue, &l.data)
		return l, err
	})
	if err != nil {
		return err
	}
	// Django walks the receivers in Python set order (random per process);
	// first appearance here. Each receiver's mail is independent.
	var receivers []string
	for _, l := range logs {
		if !slices.Contains(receivers, l.receiver) {
			receivers = append(receivers, l.receiver)
		}
	}
	var processed []string
	for _, receiver := range receivers {
		type issueGroup struct {
			issue  *string
			actors []digestActorChanges
		}
		var groups []*issueGroup
		var ids []string
		for _, l := range logs {
			if l.receiver != receiver {
				continue
			}
			var g *issueGroup
			for _, x := range groups {
				if (x.issue == nil && l.issue == nil) || (x.issue != nil && l.issue != nil && *x.issue == *l.issue) {
					g = x
				}
			}
			if g == nil {
				g = &issueGroup{issue: l.issue}
				groups = append(groups, g)
			}
			actor := "None"
			if l.actor != nil {
				actor = *l.actor
			}
			i := slices.IndexFunc(g.actors, func(x digestActorChanges) bool { return x.Actor == actor })
			if i < 0 {
				g.actors = append(g.actors, digestActorChanges{Actor: actor})
				i = len(g.actors) - 1
			}
			g.actors[i].Changes = append(g.actors[i].Changes, l.data)
			processed = append(processed, l.id)
			ids = append(ids, l.id)
		}
		for _, g := range groups {
			// Every issue's mail carries all of the receiver's log ids.
			if err := jobs.Enqueue(ctx, a.jobs, sendEmailNotificationJob{IssueID: g.issue, NotificationData: g.actors,
				ReceiverID: receiver, EmailNotificationIDs: ids}); err != nil {
				return err
			}
		}
	}
	if len(processed) > 0 {
		if _, err := a.db.Exec(ctx, `UPDATE email_notification_logs SET processed_at = now()
			WHERE id = ANY($1::text[]::uuid[]) AND deleted_at IS NULL`, processed); err != nil {
			return err
		}
	}
	return nil
}

// digestField is one field's values in create_payload's output.
type digestField struct {
	name               string
	oldValue, newValue []string
	hasOld, hasNew     bool
}

type digestActor struct {
	id           string
	fields       []*digestField
	activityTime string
	hasTime      bool
}

func (d *digestActor) field(name string) *digestField {
	for _, f := range d.fields {
		if f.name == name {
			return f
		}
	}
	f := &digestField{name: name}
	d.fields = append(d.fields, f)
	return f
}

// digestPyStr is str() of a JSON value from the log data.
func digestPyStr(raw json.RawMessage, present bool) string {
	if !present {
		return "None"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return string(raw)
}

var errDigestAbort = errors.New("send_email_notification aborted")

// digestCreatePayload ports create_payload. Its activity_time guard reads the
// literal key "actor_id", so every change overwrites the time: the last
// one wins.
func digestCreatePayload(data []digestActorChanges) ([]*digestActor, error) {
	var out []*digestActor
	find := func(id string, create bool) *digestActor {
		for _, d := range out {
			if d.id == id {
				return d
			}
		}
		if !create {
			return nil
		}
		d := &digestActor{id: id}
		out = append(out, d)
		return d
	}
	for _, actor := range data {
		for _, change := range actor.Changes {
			var c map[string]json.RawMessage
			if json.Unmarshal(change, &c) != nil {
				return nil, fmt.Errorf("%w: change is not a dict", errDigestAbort)
			}
			var ia map[string]json.RawMessage
			if raw, ok := c["issue_activity"]; !ok || json.Unmarshal(raw, &ia) != nil || len(ia) == 0 {
				continue
			}
			fieldRaw, ok := ia["field"]
			field := "\x00None" // a None key no template lookup reaches
			if ok {
				if s := digestPyStr(fieldRaw, true); string(fieldRaw) != "null" {
					field = s
				}
			}
			oldRaw, oldOK := ia["old_value"]
			newRaw, newOK := ia["new_value"]
			oldValue, newValue := digestPyStr(oldRaw, oldOK), digestPyStr(newRaw, newOK)
			if oldValue != "" {
				f := find(actor.Actor, true).field(field)
				f.hasOld = true
				if !slices.Contains(f.oldValue, oldValue) {
					f.oldValue = append(f.oldValue, oldValue)
				}
			}
			if newValue != "" {
				f := find(actor.Actor, true).field(field)
				f.hasNew = true
				if !slices.Contains(f.newValue, newValue) {
					f.newValue = append(f.newValue, newValue)
				}
			}
			d := find(actor.Actor, false)
			if d == nil {
				return nil, fmt.Errorf("%w: KeyError %s", errDigestAbort, actor.Actor)
			}
			var t string
			if json.Unmarshal(ia["activity_time"], &t) != nil {
				return nil, fmt.Errorf("%w: activity_time", errDigestAbort)
			}
			m := digestTimeRe.FindStringSubmatch(strings.TrimRight(t, "Z"))
			if m == nil {
				return nil, fmt.Errorf("%w: activity_time %q", errDigestAbort, t)
			}
			d.activityTime, d.hasTime = m[1]+":"+m[2], true
		}
	}
	return out, nil
}

// digestTimeRe reads the hour and minute fromisoformat parses.
var digestTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ](\d{2}):(\d{2})`)

// digestTime is strftime("%H:%M %p"): a 24-hour clock with AM/PM.
func digestTime(hhmm string) string {
	h, _ := strconv.Atoi(hhmm[:2])
	if h < 12 {
		return hhmm + " AM"
	}
	return hhmm + " PM"
}

// digestUnwanted is remove_unwanted_characters' class.
func digestUnwanted(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

var digestMentionRe = regexp.MustCompile(`(?is)<mention-component\b([^>]*?)/?>(?:.*?</mention-component\s*>)?`)
var digestEntityRe = regexp.MustCompile(`(?is)\bentity_identifier\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// digestProcessMention ports process_mention: each <mention-component> becomes
// "@display_name". BeautifulSoup also re-serializes the rest of the markup;
// this keeps it as written, which renders the same text.
func (a *API) digestProcessMention(ctx context.Context, html string) (string, error) {
	var failure error
	out := digestMentionRe.ReplaceAllStringFunc(html, func(tag string) string {
		if failure != nil {
			return tag
		}
		attrs := digestMentionRe.FindStringSubmatch(tag)[1]
		m := digestEntityRe.FindStringSubmatch(attrs)
		if m == nil {
			failure = fmt.Errorf("%w: KeyError entity_identifier", errDigestAbort)
			return tag
		}
		id, ok := digestParseUUID(m[1] + m[2] + m[3])
		if !ok {
			failure = fmt.Errorf("%w: mention %q", errDigestAbort, m[1]+m[2]+m[3])
			return tag
		}
		var name string
		if err := a.db.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, id).Scan(&name); err != nil {
			failure = fmt.Errorf("%w: mentioned user: %v", errDigestAbort, err)
			return tag
		}
		return djangoEscaper.Replace("@" + name)
	})
	return out, failure
}

func digestParseUUID(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(s))
	return id, err == nil
}

func (a *API) digestProcessHTML(ctx context.Context, values []string, present bool) (any, error) {
	if !present {
		return nil, nil
	}
	out := make([]any, 0, len(values))
	for _, v := range values {
		p, err := a.digestProcessMention(ctx, v)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// digestUser is what the mail reads from a User.
type digestUser struct {
	email, firstName, lastName, displayName string
	avatarURL                               string // the avatar_url property; "None" when unset
}

func (a *API) digestUser(ctx context.Context, id string) (*digestUser, error) {
	uid, ok := digestParseUUID(id)
	if !ok {
		return nil, fmt.Errorf("%w: user %q", errDigestAbort, id)
	}
	var u digestUser
	var asset *uuid.UUID
	var avatar *string
	err := a.db.QueryRow(ctx, `SELECT coalesce(email, ''), first_name, last_name, display_name, avatar_asset_id, avatar
		FROM users WHERE id = $1`, uid).Scan(&u.email, &u.firstName, &u.lastName, &u.displayName, &asset, &avatar)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: User.DoesNotExist", errDigestAbort)
	}
	if err != nil {
		return nil, err
	}
	switch {
	case asset != nil:
		u.avatarURL = "/api/assets/v2/static/" + asset.String() + "/"
	case avatar != nil && *avatar != "":
		u.avatarURL = *avatar
	default:
		u.avatarURL = "None"
	}
	return &u, nil
}

// digestLockID is the task's Redis lock key.
func digestLockID(j sendEmailNotificationJob) string {
	ids := slices.Clone(j.EmailNotificationIDs)
	slices.Sort(ids) // sorted UUIDs: the same order as their lowercase text
	issue := "None"
	if j.IssueID != nil {
		issue = *j.IssueID
	}
	return "send_email_notif_" + issue + "_" + j.ReceiverID + "_" + strings.Join(ids, "_")
}

// runSendEmailNotification ports send_email_notification. The Redis lock
// (5 minutes, released when done) keeps two runs for the same logs from
// both mailing. Unlike Django, links use the configured app URL instead of
// the request origin issue_activity caches in Redis for 10 minutes, so a
// digest that runs later than that still sends (DEVIATIONS: issue origin
// cache).
func (a *API) runSendEmailNotification(ctx context.Context, j sendEmailNotificationJob) error {
	lock := a.cfg.RedisKeyPrefix + digestLockID(j)
	ok, err := a.rdb.SetNX(ctx, lock, "true", 300*time.Second).Result()
	if err != nil {
		return err
	}
	if !ok {
		a.log.Info("Duplicate email received skipping")
		return nil
	}
	release := func() { _ = a.rdb.Del(context.WithoutCancel(ctx), lock).Err() }
	err = a.sendDigest(ctx, j)
	release()
	if errors.Is(err, errDigestAbort) {
		a.log.Error("send_email_notification", "err", err)
		return nil
	}
	return err
}

func (a *API) sendDigest(ctx context.Context, j sendEmailNotificationJob) error {
	baseAPI := a.baseHost(true)
	data, err := digestCreatePayload(j.NotificationData)
	if err != nil {
		return err
	}
	receiver, err := a.digestUser(ctx, j.ReceiverID)
	if err != nil {
		return err
	}
	if j.IssueID == nil {
		return fmt.Errorf("%w: Issue.DoesNotExist", errDigestAbort)
	}
	issueID, ok := digestParseUUID(*j.IssueID)
	if !ok {
		return fmt.Errorf("%w: issue %q", errDigestAbort, *j.IssueID)
	}
	var issue struct {
		name, identifier, project, slug string
		seq                             int
		projectID                       uuid.UUID
	}
	err = a.db.QueryRow(ctx, `SELECT i.name, i.sequence_id, p.identifier, p.name, p.id, w.slug
		FROM issues i JOIN projects p ON p.id = i.project_id JOIN workspaces w ON w.id = p.workspace_id
		WHERE i.id = $1 AND i.deleted_at IS NULL`, issueID).
		Scan(&issue.name, &issue.seq, &issue.identifier, &issue.project, &issue.projectID, &issue.slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: Issue.DoesNotExist", errDigestAbort)
	}
	if err != nil {
		return err
	}
	identifier := issue.identifier + "-" + strconv.Itoa(issue.seq)

	var templateData, comments []any
	var actorsInvolved []string
	for _, d := range data {
		actor, err := a.digestUser(ctx, d.id)
		if err != nil {
			return err
		}
		detail := map[string]any{"avatar_url": baseAPI + actor.avatarURL, "first_name": actor.firstName,
			"last_name": actor.lastName}
		changes := map[string]any{}
		var comment, mention *digestField
		for _, f := range d.fields {
			switch f.name {
			case "comment":
				comment = f
				continue
			case "mention":
				mention = f
				continue
			}
			changes[f.name] = digestFieldMap(f)
		}
		actorsInvolved = append(actorsInvolved, d.id)
		if comment != nil {
			comments = append(comments, map[string]any{"actor_comments": digestFieldMap(comment), "actor_detail": detail})
		}
		if mention != nil {
			m := digestFieldMap(mention)
			if m["new_value"], err = a.digestProcessHTML(ctx, mention.newValue, mention.hasNew); err != nil {
				return err
			}
			if m["old_value"], err = a.digestProcessHTML(ctx, mention.oldValue, mention.hasOld); err != nil {
				return err
			}
			comments = append(comments, map[string]any{"actor_comments": m, "actor_detail": detail})
		}
		if !d.hasTime {
			return fmt.Errorf("%w: KeyError activity_time", errDigestAbort)
		}
		if len(changes) > 0 {
			templateData = append(templateData, map[string]any{
				"actor_detail": detail, "changes": changes,
				"issue_details": map[string]any{"name": issue.name, "identifier": identifier},
				"activity_time": digestTime(d.activityTime),
			})
		}
	}
	slices.Sort(actorsInvolved)
	issueURL := baseAPI + "/" + issue.slug + "/projects/" + issue.projectID.String() + "/issues/" + issueID.String()
	ctxData := map[string]any{
		"data":            templateData,
		"summary":         "Updates were made to the issue by",
		"actors_involved": len(slices.Compact(actorsInvolved)),
		"issue":           map[string]any{"issue_identifier": identifier, "name": issue.name, "issue_url": issueURL},
		"receiver":        map[string]any{"email": receiver.email},
		"issue_url":       issueURL,
		"project_url":     baseAPI + "/" + issue.slug + "/projects/" + issue.projectID.String() + "/issues/",
		"workspace":       issue.slug,
		"project":         issue.project,
		"user_preference": baseAPI + "/" + issue.slug + "/settings/account/notifications/",
		"comments":        comments,
		"entity_type":     "issue",
	}
	html, err := mail.RenderDjango("notifications/issue-updates.html", ctxData)
	if err != nil {
		return err
	}
	subject := identifier + " " + digestUnwanted(issue.name)
	if err := a.mailer.Send(ctx, []string{receiver.email}, subject, mail.PlainText(html), html); err != nil {
		// Django logs and gives up; the job's retries try again (the lock is
		// released first).
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE email_notification_logs SET sent_at = now()
		WHERE id = ANY($1::text[]::uuid[]) AND deleted_at IS NULL`, j.EmailNotificationIDs); err != nil {
		// The mail is out: a retry would send it twice.
		a.log.Error("send_email_notification: marking logs sent", "err", err)
	}
	return nil
}

// digestFieldMap is a create_payload field entry ({"old_value": [...],
// "new_value": [...]}, either key absent when it had no values).
func digestFieldMap(f *digestField) map[string]any {
	m := map[string]any{}
	if f.hasOld {
		m["old_value"] = f.oldValue
	}
	if f.hasNew {
		m["new_value"] = f.newValue
	}
	return m
}
