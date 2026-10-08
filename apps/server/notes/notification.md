# Notifications (PORTING section 14)

## Ported

Code: `internal/api/notification.go`, routes in `internal/api/routes_notification.go`.
Golden `notifications` (225 steps, `contract/notification_test.go`) covers all of them.

- `GET W/users/notifications/` (filters, `is_mentioned_notification`, ordering, offset pagination)
- `GET`, `PATCH W/users/notifications/<pk>/`
- `POST`, `DELETE W/users/notifications/<pk>/read/`
- `POST`, `DELETE W/users/notifications/<pk>/archive/`
- `GET W/users/notifications/unread/`
- `POST W/users/notifications/mark-all-read/`
- `GET`, `PATCH /api/users/me/notification-preferences/`

## Not ported

- `DELETE W/users/notifications/<pk>/` (`NotificationViewSet.destroy`): UNUSED. Django answers 204 and
  soft-deletes; Go answers 405.

## Quirks ported on purpose

- `snoozed`/`archived` other than `true`/`false` is a KeyError (400 "The required key does not exist.").
- `?mentioned=` is truthy for any non-empty value (`mentioned=false` filters mentions).
- Pagination only when both `per_page` and `cursor` are non-empty; otherwise the plain array.
- `type=subscribed` is meant to drop issues assigned to the user, but compares the assignee row's own id with the
  issue id, so it never matches. Only issues created by the user are dropped. `type=created` for a workspace
  guest (role < 15) is an empty queryset, even combined with other types.
- The retrieve/read/archive/PATCH responses omit `is_inbox_issue`, `is_intake_issue` and
  `is_mentioned_notification` (they are only annotated by the list). `retrieve` of a foreign or missing id is
  `{"detail": "No Notification matches the given query."}`; the others are the generic `.get` 404.
- `mark-all-read` uses `bulk_update(["read_at"])`: `updated_at`/`updated_by` are not touched. `type` only
  matches the strings `watching`, `assigned`, `created` (not `subscribed`). `snoozed`/`archived` use Python
  truthiness of the JSON value. A non-dict body is a 500.
- `PATCH <pk>/` reads only `snoozed_till` (null when missing); a non-dict body is a 500 after the 404 check.
- Preferences: `user`, `workspace`, `project`, `created_by`, `updated_by` and `deleted_at` are writable.
  Moving the row to another user gives that user two rows, so their GET/PATCH is a 500
  (MultipleObjectsReturned); the previous owner gets 404. `deleted_at` hides the row.

## Deviations

None known. `is_inbox_issue`/`is_intake_issue` run Django's EXISTS over `intake_issues` (always false, the table
is empty because intake is cut).

## Shared files changed

None.

## Unsure / for the lead

- Notification rows written by `internal/api/issue_activity.go` have `updated_at == created_at`; Django's
  `bulk_create` leaves `updated_at` a few microseconds later. I dropped that comparison from my DB rows
  (it is not an API-visible difference except through `updated_at` values, which the goldens normalise).
- Order between two notifications created in one task from a Python set (e.g. one description edit mentioning
  two users, both notifying the same receiver) varies in Django with PYTHONHASHSEED, so the scenario mentions
  one user per edit.
- The scenario cannot use `issues/<id>/subscribe/` (not ported when written); subscribers come from assignees.
