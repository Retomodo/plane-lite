# Favorites & recents (PORTING.md section 15)

Code: `internal/api/favorite.go`, `internal/api/routes_favorite.go`. Scenarios: `contract/favorite_test.go`
(`TestFavoritesWorkspace`, `TestFavoritesEntity`, `TestRecentVisits`).

## 1. Endpoints ported, and goldens

| Endpoint | Golden |
|---|---|
| `GET`/`POST W/user-favorites/` | `favorite_workspace` |
| `PATCH`/`DELETE W/user-favorites/<favorite_id>/` | `favorite_workspace` |
| `GET W/user-favorites/<favorite_id>/group/` | `favorite_workspace` |
| `POST`/`DELETE P/user-favorite-cycles/…`, `…-modules/…`, `…-views/…` | `favorite_entities` |
| `POST P/favorite-pages/<page_id>/` | `favorite_entities` |
| `POST W/user-favorite-projects/`, `DELETE W/user-favorite-projects/<project_id>/` | `favorite_entities` |
| `GET W/recent-visits/` (`?entity_name=`) | `recent_visits` |

`favorite_workspace` also pins the list's entity_data per type, the membership join (below), soft-deleted
entities and projects, and a cross-workspace favorite. `recent_visits` covers every role, the filter
variants, deleted/archived entities, a second workspace, the cap of 20 on writes (22 issue visits through the
API) and the `LIMIT 20` on reads (25 visits seeded by SQL).

## 2. Not ported (UNUSED)

- `GET` of `P/user-favorite-cycles/`, `-modules/`, `-views/` and `W/user-favorite-projects/`: Go answers 405
  (the URLs have other methods).
- `DELETE P/favorite-pages/<page_id>/`: Django answers 204; Go 405.
- `GET`/`POST W/user-favorites/<favorite_id>/` and `PATCH`/`DELETE W/user-favorites/` (no id): Django answers
  500 (the view methods do not take the argument); Go answers 405 / 404.

## 3. Deviations

None for the ported endpoints. Quirks kept (all recorded against Django):

- `UserFavorite.save()`: the new sequence is the largest live sequence of the workspace (every user's
  favorites) plus 10000, and the given one only counts when the workspace has none. A project replaces the
  workspace (`self.project` uses the base manager, so a soft-deleted project works and a project of another
  workspace moves the favorite there).
- `W` POST: a repeat of (workspace, user, type, entity) answers the existing favorite with 200 (looked up
  only for a truthy `entity_identifier`); a unique-index hit (same entity favorited in another workspace)
  answers 400 `Favorite already exists`. `project_id` is a read-only attribute, so the response echoes the
  raw string the request sent. Non-dict bodies are a 500.
- The per-entity POSTs (and the page one, which ignores the body) never check that the entity exists or is in
  the project, accept `null`/missing ids (a NULL entity row), ints as `UUID(int=n)`, and answer 400 `The
  payload is not valid` on a duplicate, 400 `Please provide valid detail` on a bad uuid.
- `W/user-favorite-projects/` needs only a signed-in user (not workspace membership, not even a real slug).
  A missing `project` is a 404 (`RelatedObjectDoesNotExist`).
- The lists join `project_members` with no `deleted_at` filter: a soft-deleted but still active membership row
  repeats the favorite; favorites of soft-deleted projects stay in the list. The root list also drops project-less
  `page` favorites.
- Hard deletes cascade through the `parent` foreign key, soft-deleted children included.
- Recent visits use their own serializers (not the favorite ones): issues are included (assignees newest user
  first), projects list their active non-bot members newest first, pages carry owner and project. The list is
  ordered by `created_at`, not `visited_at`.

## 4. Shared files

None. No change to harness, `drf`, `httpx` or other areas' scenarios and goldens; `projectLitePerm` (from the
issue-discussion port) and `cycleUUIDs` are reused as they are.

## 5. Unsure about

- `entity_type` in the W POST lookup is compared as `PyStr(value)`; list/dict values only matter if a row with
  that exact Python repr as its type exists. Not tested.
- Strings with NUL characters (Postgres rejects them, Django would raise a different error) are not covered.
