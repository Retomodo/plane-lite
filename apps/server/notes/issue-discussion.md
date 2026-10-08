# Issue discussion (PORTING.md section 7: history, comments, subscribe, reactions)

## 1. Endpoints ported

| Method | URL | Django view | Golden |
|---|---|---|---|
| GET | `P/issues/<issue_id>/history/` | `IssueActivityEndpoint.get` | issue_comments, issue_reactions |
| POST | `P/issues/<issue_id>/comments/` | `IssueCommentViewSet.create` | issue_comments (also issue_reactions, issue_subscribe) |
| PATCH | `P/issues/<issue_id>/comments/<pk>/` | `IssueCommentViewSet.partial_update` | issue_comments |
| DELETE | `P/issues/<issue_id>/comments/<pk>/` | `IssueCommentViewSet.destroy` | issue_comments, issue_reactions |
| GET | `P/issues/<issue_id>/subscribe/` | `IssueSubscriberViewSet.subscription_status` | issue_subscribe |
| POST | `P/issues/<issue_id>/subscribe/` | `IssueSubscriberViewSet.subscribe` | issue_subscribe |
| DELETE | `P/issues/<issue_id>/subscribe/` | `IssueSubscriberViewSet.unsubscribe` | issue_subscribe |
| GET | `P/issues/<issue_id>/reactions/` | `IssueReactionViewSet.list` | issue_reactions |
| POST | `P/issues/<issue_id>/reactions/` | `IssueReactionViewSet.create` | issue_reactions |
| DELETE | `P/issues/<issue_id>/reactions/<str:reaction_code>/` | `IssueReactionViewSet.destroy` | issue_reactions |
| GET | `P/comments/<comment_id>/reactions/` | `CommentReactionViewSet.list` | issue_reactions |
| POST | `P/comments/<comment_id>/reactions/` | `CommentReactionViewSet.create` | issue_reactions |
| DELETE | `P/comments/<comment_id>/reactions/<str:reaction_code>/` | `CommentReactionViewSet.destroy` | issue_reactions |

Goldens: `issue_comments` (105 steps), `issue_reactions` (99), `issue_subscribe` (60), all in
`contract/issue_discussion_test.go`. Each ends with `discussionRows`, which records issues (updated_at
bump), issue_comments with their descriptions, descriptions, issue_reactions, comment_reactions,
issue_activities, issue_subscribers, issue_mentions, notifications and email_notification_logs.

Activity trackers ported (`internal/api/issue_activity_discussion.go`): `comment.activity.created/updated/deleted`,
`issue_reaction.activity.created/deleted`, `comment_reaction.activity.created/deleted`. The `notifications`
task needed no change: batch 8's port already handles comment activities (comment mentions, subscriber
notifications, email logs) and returns early for the reaction types.

## 2. Not ported

- UNUSED: `GET P/issues/<issue_id>/comments/`, `GET`/`PUT P/issues/<issue_id>/comments/<pk>/`
  (DRF default list/retrieve/update), and the `P/issues/<issue_id>/issue-subscribers/` routes (list,
  create, destroy). Go answers 405 for those methods.
- Cut: `model_activity` (webhooks) after comment create/update.

## 3. Deviations

- `history/` is wrapped in Django's `gzip_page`; Go does not gzip. The body is identical once decoded
  (the contract client decompresses transparently), only `Content-Encoding` differs.
- The Redis `issue_id -> origin` key that `issue_activity` sets is not written (as in batch 8).

Quirks kept on purpose (Django's behaviour):

- A new comment's response carries `updated_by` = the actor while the row has `updated_by` NULL
  (`IssueComment.save()` re-saves with `update_fields=["description_id"]`, which sets `updated_by` in
  memory only). Later reads (history) show NULL.
- `is_member` is absent from every comment body (never annotated in these views).
- Comment create looks the issue up in any project (`Issue.objects.get(pk=issue_id)`), so a comment
  can belong to the URL project but another project's issue; `history/` filters by issue and by the
  user's membership of each row's project, not by the URL project.
- The PATCH activity's `new_value` is the raw request `comment_html` (before nh3), and a PATCH without
  `comment_html` still records "updated a comment" with `new_value` "". `actor`, `parent`,
  `description`, `deleted_at`, `edited_at` are writable through the serializer.
- Comment reaction create reports any IntegrityError (unknown comment included) as
  "Reaction already exists for the user"; issue reaction / subscribe on an unknown issue are
  400 "The payload is not valid" (FK violation).
- `issue_reaction.activity.created` / `comment_reaction.activity.created` take the actor's newest
  reaction with that code anywhere in the project; a reaction added to a deleted comment records no
  activity (the tracker raises).
- `created_at__gt` on history: naive values are UTC (`get_default_timezone`), date-only values are
  midnight; unparsable (or empty) values give 400 "Please provide valid detail".

## 4. Shared files changed

- `internal/api/issue_activity.go`: one `case` added to the type switch in `issueActivity`, after
  `issue.activity.deleted`, listing the seven comment/reaction types and calling
  `t.discussionActivity(ctx)` (defined in `issue_activity_discussion.go`). Nothing else changed.
- `internal/drf/fields.go`: new `Validator.CharList(name, child CharField, maxItems)` for a
  `ListField(child=CharField/URLField)` plus an ArrayField's `ArrayMaxLengthValidator` (comment
  `attachments`). Additive.
- `internal/api/routes_issue_discussion.go`: routes (no jobs to register).

## 5. Unsure / notes

- `history/` joins `project_members` without the manager filter, as Django does, so a user with two
  matching active membership rows for a project would see each row twice. Not exercised.
- The `description` UniqueValidator message ("Issue Comment with this description already exists.") is
  covered by the golden; a comment without a description (legacy rows) gets one created on PATCH or
  DELETE, as `IssueComment.save()` does. That path is not exercised (the API always creates one).
- History bodies mask `epoch` (`int(timezone.now().timestamp())`, wall-clock time).
- Known unrelated failure in this worktree: `TestIssueBulk` (`archived_at` recorded on 2026-10-07, now
  2026-10-08); fixed by the lead in the main tree. Every other test passes.
