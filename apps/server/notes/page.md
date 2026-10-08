# Pages (PORTING.md section 12)

## 1. Endpoints ported and goldens

| Endpoint | Golden |
|---|---|
| `GET`, `POST P/pages/` | `page_crud` (121 steps) |
| `GET`, `PATCH`, `DELETE P/pages/<page_id>/` | `page_crud`, `page_lifecycle` (126 steps) |
| `POST`, `DELETE P/pages/<page_id>/archive/` | `page_lifecycle` |
| `POST`, `DELETE P/pages/<page_id>/lock/` | `page_lifecycle` |
| `POST P/pages/<page_id>/access/` | `page_lifecycle` |
| `POST P/pages/<page_id>/duplicate/` | `page_lifecycle` |
| `GET`, `PATCH P/pages/<page_id>/description/` | `page_description` (109 steps) |
| `GET P/pages/<page_id>/versions/` and `.../versions/<pk>/` | `page_description` |

Files:
- `internal/api/page.go`: the permission, the queryset, list/create/retrieve/patch/delete.
- `internal/api/page_action.go`: archive, lock, access and duplicate.
- `internal/api/page_description.go`: the description endpoints and versions.
- `internal/api/page_task.go`: the jobs.
- `internal/api/page_html.go`: a port of Python 3.12 `html.parser` and BeautifulSoup 4.12.3.
- `routes_page.go`.
- `contract/page_test.go`.

The tasks are River jobs:
- `page_transaction`: the mention-component and image-component rows in `page_logs`.
- `track_page_version`.
- `copy_s3_objects_of_description_and_assets`, for duplicates.

`page_html.go` exists because `strip_tags` (used for `description_stripped` on every `Page.save()`) and the tasks' BeautifulSoup parsing must match Python byte for byte. It is unit-tested against 52 Python-generated cases:
- `internal/api/page_html_test.go`;
- `internal/api/testdata/page_html.json`, regenerated with `scripts/devstack.sh 4 shell < contract/reference/gen_page_html_fixtures.py | sed -n 's/^FIXTURES//p' > internal/api/testdata/page_html.json`.

The fixtures cover:
- charrefs and entities;
- CDATA and script content;
- void tags;
- attribute sorting and serialization;
- the surrogate and NUL cases.

apps/live: no changes needed. It calls:
- `GET`/`PATCH P/pages/<id>/`;
- `GET`/`PATCH .../description/`;
- `/pages/<id>/mentions/`, which does not exist in Django either, so it 404s in both.

## 2. Not ported

- `GET P/pages-summary/`: UNUSED.
- `P/favorite-pages/<page_id>/`: section 15 (favorites). The tests insert `user_favorites` rows by SQL.

## 3. Deviations

- **Asset copy on duplicate.** Django copies the image-component assets on S3 and rewrites their ids. Go skips that copy (assets are a later batch) and only logs a warning if matching live `file_assets` exist. Everything else in the task is ported:
  - an invalid src UUID aborts the task;
  - the html is re-serialized with `str(soup)` and saved;
  - the live `convert-document` sync runs.
- **Copy-task user.** In the reference, Celery runs eagerly, so the copy task's `save()` records the request user as `updated_by`, and the golden pins that. Go passes the user in the job and does the same in production. Real asynchronous Celery would have no current user there.
- **Live URL path.** The live `convert-document` URL is `<scheme://host of LIVE_BASE_URL>/live/convert-document/`, with the path as a constant. Django builds it from `LIVE_URL`, which includes `/live`.
  - In tests that host (localhost:3100) is unreachable in both Django and Go, so neither gets a 200.
  - The lead pinned the Go test server's `LiveBaseURL` to a port that always refuses (`contract/target.go`), so a dev live server on :3100 can no longer make the duplicate step diverge.
- **Search terms.** The agent's DRF 3.17 `search_smart_split` port became the shared `drf.SearchTerms` at merge (the old one predated quoted phrases and the null-character 400); the workspace list and workspace member searches use it too.
- **Approximations in `page_html.go`:**
  - `pageLower` is Unicode `ToLower` without Python's final-sigma rule;
  - the case-insensitive end-tag match in CDATA mode is ASCII-only.

  None of this shows up with realistic editor HTML.
- No Redis cache was involved.

## 4. Shared-file changes (additive)

- `internal/sanitize/html5/lookup.go`: `LookupEntity(name)`, which exposes the HTML5 entity table to the `html.unescape` port.
- `internal/drf/uuidlist.go`: `Validator.UUIDList`, a ListField of UUIDField, with indexed child errors.

## 5. Quirks ported (see the goldens)

- **Permission** (`ProjectPagePermission`):
  - Any active project member may read.
  - POST, PUT and PATCH need an admin or member; DELETE needs an admin.
  - The page owner always passes. A private page is 403 for everyone but its owner.
  - A page not live-linked to the URL project is 403.
- **List and retrieve queryset:**
  - `label_ids` keeps soft-deleted page labels.
  - `project_ids` only lists the projects where the user is an active member.
  - The order is `is_favorite DESC`, then the sanitized `order_by`, then `id`.
  - Guests without `guest_view_all_features`:
    - list: only their own pages;
    - retrieve: 400 for others' pages, 500 when the page is missing.
- **Create:**
  - `description_html`, `description_json` and `description_binary` come raw from `request.data`. A null html or json gives 400 (NOT NULL). A non-string html or any binary gives 500.
  - A valid `label_ids` or `project_ids` gives 500.
  - The response re-reads through `get_queryset`. A child page, or a `?search=` that excludes the new page, gives 404 after the page has been created.
- **PATCH:**
  - `parent` and `access` ownership checks use Python truthiness and equality.
  - The new labels get the instance's old `created_by` and `updated_by`.
  - A `created_by` in the request sticks.
  - `label_ids` and `project_ids` are echoed only when they were sent.
- **Access:**
  - The value goes through `SmallIntegerField`'s `int()`:
    - `"1"` and `true` become 1;
    - `7` is stored as 7;
    - `-1` breaks the check constraint, giving 400;
    - null gives 400 (NOT NULL);
    - `"x"`, lists and out-of-range values give 500.
  - A list body gives 500.
- **Archive and unarchive:**
  - The descendant CTE also touches deleted pages.
  - `archived_at` in the response is `str(datetime.now())`.
  - Unarchive detaches the page from a parent that is still archived, even a deleted parent.
- **Delete:**
  - The page must be archived. The owner or a project admin may delete it.
  - Children in the project are detached.
  - Favorites are soft-deleted and recent visits hard-deleted.
- **Duplicate:**
  - Copies every column, including archived/locked state, the parent and the json, but not `description_binary`.
  - Links the copy to every live project link of the original. The response's `project_ids` include deleted links.
- **`page_transaction`:**
  - Inserts with `ON CONFLICT DO NOTHING`.
  - Any non-UUID id or identifier aborts the whole batch.
  - Removals soft-delete the `page_logs` rows with the removed transaction ids on every page.
- **`track_page_version` never writes:** Django reads the nonexistent `page.description`, and the AttributeError is swallowed. The job is a logged no-op.
- **Description PATCH:**
  - Error codes: 4701 when the page is locked, 4702 when archived.
  - `description_binary`:
    - decoded with Python's non-strict base64;
    - then checked by `validate_binary_data`: size, a 4-byte minimum, and suspicious patterns in the first 200 characters;
    - `""` gives 500 (a str in a BinaryField).
  - `description_html` is sanitized with nh3 (`sanitize.HTML`) when non-empty.
  - A null `description_json` gives 400.
- **Description GET:** returns raw bytes as `application/octet-stream`, filename `page_description.bin`.

## 6. Masks, Unordered, SortBy, SQL in the tests

- **`Unordered`** on `label_ids` and `project_ids` (`pageIDs`): `array_agg(DISTINCT uuid)` order follows random ids.
- **`SortBy("", "name")`** on `?order_by=sort_order`: every page has the same `sort_order`, so Django's order falls to the random `id`.
- **`Mask("archived_at")`** on archive responses: wall clock.
- **SQL writes:**
  - `user_favorites` INSERTs: the favorites endpoints are section 15.
  - A second `project_pages` link: there is no endpoint for multi-project pages.
  - `UPDATE pages SET description_binary`: so duplicate can show the binary being dropped.
  - `page_versions` INSERTs: Django never creates versions, as above.
- **Read-only SQL:** the `SELECT id` lookups and `pageRows` DBRows.
- Bob's duplicate is renamed so that the name-ordered lists are deterministic.

## 7. Unsure

- The live sync risk in section 3.
- `Page.objects.get(...)` with several live links to the same project would be a 500 (MultipleObjectsReturned) in Django; Go takes the first row. This is unreachable through the API.
