# Estimates and stickies/home (PORTING.md sections 10 and 13)

## 1. Endpoints ported and goldens

| Endpoint | Golden |
|---|---|
| `GET`, `POST P/estimates/`; `DELETE P/estimates/<estimate_id>/` | `estimates` (111 steps) |
| `POST P/estimates/<id>/estimate-points/`; `PATCH .../estimate-points/<estimate_point_id>/` | `estimates` |
| `GET W/estimates/` | `estimates` |
| `GET`, `POST W/quick-links/`; `PATCH`, `DELETE W/quick-links/<pk>/` | `quick_links` (81 steps) |
| `GET W/home-preferences/`; `PATCH W/home-preferences/<key>/` | `home_preferences` (47 steps) |
| `GET`, `POST W/stickies/`; `PATCH`, `DELETE W/stickies/<pk>/` | `stickies` (90 steps) |

Files: `internal/api/{estimate,home}.go`, `routes_estimate.go`, `routes_home.go`,
`contract/{estimate,home}_test.go`.

## 2. Not ported (UNUSED)

`GET P/project-estimates/`, `GET` and `PATCH P/estimates/<estimate_id>/`,
`DELETE P/estimates/<id>/estimate-points/<point_id>/`, `GET W/quick-links/<pk>/`,
`GET W/stickies/<pk>/`, `PATCH W/home-preferences/` (no key; Django 500s) and
`GET W/home-preferences/<key>/` (Django 500s). Their methods answer 405 in Go.
`W/recent-visits/` is section 15.

## 3. Response cache (dropped at merge)

The agent ported `cache_response`/`invalidate_cache` as `internal/api/rcache.go`, keeping Django's
quirk that estimate writes invalidate a key that never exists, so `W/estimates/` stayed stale for 2 h.
At merge the lead removed it, in line with the earlier `GET /api/instances/` and `W/labels/`
deviations: `W/estimates/` is the last kept `cache_response` view, so Go computes it per request.
The scenario reads `W/estimates/` with a fresh query string each time, which keeps every Django read
uncached (the key is the full path). See DEVIATIONS.md.

## 4. Deviations

- `GET W/estimates/` is not cached (above).

## 5. Shared-file changes

- `internal/api/paginate.go`: added `parsePageParamsDefault` and `parseOffsetPageDefault` (the
  view's `default_per_page`); the old functions call them with 1000. Stickies use 20.

## 6. Notes and quirks ported (see the goldens)

- Estimate create writes the estimate before validating `estimate_points`; a rejected body leaves the
  estimate behind. Points are inserted from the raw request values (untrimmed, `created_by` and
  `updated_by` both the requester). Non-dict `estimate`/body: 500.
- Point create (`EstimatePointEndpoint`) takes raw values: `key` goes through `int()` (bad ones 500),
  `value` through `str()` (lists become Python reprs).
- Point PATCH accepts `deleted_at` and `created_by`; `estimate_point_id` is an untyped route
  segment, so a non-UUID is Django's "Please provide valid detail" 400.
- Quick links: the `http://` prefix, `{"url": {"error": ...}}` error shape, duplicate URL check, and a
  link with a project takes that project's workspace on every save.
- Home preferences GET: the i-th missing widget gets sort_order `1000 - i`; rows come back newest
  first (inserted with `clock_timestamp()`). PATCH can rename `key`.
- Stickies: creator check runs before `get_object`; `created_by` is writable on PATCH, so a changed
  creator gets 403 from the creator check (old creator) or 404 from `get_object` (new creator).
  `sort_order` on create is max over the workspace's live stickies + 10000, overriding input.

## 7. Masks, Unordered, SortBy

None added.

## 8. Unsure

- Estimate list order ties (same `name` across projects in `W/estimates/`) and equal point `value`s
  are unspecified in Django; the scenarios avoid them.
