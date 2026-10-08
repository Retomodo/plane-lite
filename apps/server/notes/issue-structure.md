# Issue structure (PORTING.md section 7: sub-issues, links, relations, description versions)

## 1. Endpoints ported

| Method | URL | Django view | Golden |
|---|---|---|---|
| GET | `P/issues/<issue_id>/sub-issues/` | `SubIssuesEndpoint.get` | `sub_issues` |
| POST | `P/issues/<issue_id>/sub-issues/` | `SubIssuesEndpoint.post` | `sub_issues` |
| GET | `P/issues/<issue_id>/issue-links/` | `IssueLinkViewSet.list` | `issue_links` |
| POST | `P/issues/<issue_id>/issue-links/` | `IssueLinkViewSet.create` (+ `crawl_work_item_link_title`) | `issue_links` |
| PATCH | `P/issues/<issue_id>/issue-links/<pk>/` | `IssueLinkViewSet.partial_update` | `issue_links` |
| DELETE | `P/issues/<issue_id>/issue-links/<pk>/` | `IssueLinkViewSet.destroy` | `issue_links` |
| GET | `P/issues/<issue_id>/issue-relation/` | `IssueRelationViewSet.list` | `issue_relations` |
| POST | `P/issues/<issue_id>/issue-relation/` | `IssueRelationViewSet.create` | `issue_relations` |
| POST | `P/issues/<issue_id>/remove-relation/` | `IssueRelationViewSet.remove_relation` | `issue_relations` |
| GET | `P/work-items/<work_item_id>/description-versions/[<pk>/]` | `WorkItemDescriptionVersionEndpoint.get` | `issue_description_versions` |

Goldens (in `contract/issue_structure_test.go`): `issue_links` (85 steps), `issue_relations` (94),
`sub_issues` (101), `issue_description_versions` (73). Each one covers every role, bad bodies (list bodies,
wrong types, missing keys), dead and cross-project ids, archived and draft issues. They also record
`issue_links`, `issue_relations` and everything `issueRows` records (activities, notifications, email logs,
subscribers).

Files: `internal/api/issue_link.go`, `issue_link_crawl.go` (+ `_test.go`), `issue_relation.go`,
`issue_sub.go`, `issue_description_version.go`, `issue_activity_structure.go` and
`routes_issue_structure.go`.

## 2. Not ported

- `GET`/`PUT P/issues/<issue_id>/issue-links/<pk>/` (retrieve, update): **UNUSED**. The route answers 405.
- `GET P/issues/<issue_id>/versions/[<pk>/]` (`IssueVersionEndpoint`): **UNUSED**.
- `model_activity` is not called (it only feeds webhooks, which are cut).

## 3. Deviations and notes

- **Link crawler and the network.** `crawl_work_item_link_title` is a River job, `crawlLinkJob`, run inline
  in tests like `.delay()`. The Go port follows the Python closely:
  - the SSRF guard: `resolve_and_validate`, pinned connections, and manual redirects with a 5-hop limit,
    each hop re-checked;
  - the timeouts: 1 s for the page, 2 s for the `HEAD /favicon.ico` probe;
  - the page parsing: the first `<title>`, and the `link[rel=...]` icon selectors in order;
  - the fallbacks: the default lucide icon, and the same metadata shape
    `{title, favicon, url, favicon_url}`.

  The scenarios only use URLs that fail the guard or DNS (`localhost`, `127.0.0.1`, `10.x`,
  `nowhere.invalid`). They store `{"title": null, "favicon": <default>, "url": ..., "favicon_url": null}`
  on both sides. Fetching a real page is ported but not recorded; I checked it by hand against
  `https://example.com/` (title "Example Domain"). The known differences on real pages:
  - Go parses with `x/net/html`, Django with BeautifulSoup's `html.parser`, so malformed markup can
    disagree;
  - Go reads at most 10 MiB of a response, where `requests` reads it all;
  - `is_blocked_ip` is a CIDR table copied from Python 3.12's `ipaddress` lists plus the deny-list. Every
    IPv6 transition prefix (::ffff:0:0/96, 64:ff9b::/96, 2002::/16, 2001::/32) is blocked outright, which
    is what the embedded-IPv4 recursion amounts to;
  - the "Unexpected error" metadata branch (an exception outside the fetch) has no Go equivalent.
- **Who the crawl's `save()` records.** In the eager reference, the crawl's `save()` sets
  `updated_by` = request user, and the golden records that. The job therefore carries the actor. Under real
  async Celery, crum has no user, so `save()` would set both `created_by` and `updated_by` to NULL. That is
  a production-only Django behaviour we deliberately don't copy.
- **Relation list order.** The bucket querysets have no ORDER BY (GROUP BY drops `Meta.ordering`), so rows
  come in plan order, which follows the random issue ids. Each bucket is compared with `SortBy(name)`, and
  the comment in the test says why. Go runs the same SQL (captured with `capture-sql.sh`).
- **Sub-issue `group_by`.** The scenario leaves out `completed_at` (its key is a raw timestamp) and
  `label_ids` (its `[UUID(...), ...]` repr follows the random label ids). Both are ported: `groupKey`
  renders Python's `str()` for every `.values()` field.
- Quirks ported as-is (all recorded):
  - **Links:**
    - A PATCH without `url` logs "updated a link" with `new_value` "".
    - The activity compares against the raw request URL, before `http://` is prepended.
    - Links can point at another project's issue.
    - A dead issue gives 400 "The payload is not valid".
  - **Relation create:**
    - It answers with every requested issue, including pairs skipped by `ignore_conflicts`.
    - Its activities use the raw requested ids, so an archived issue gets "added … relation" activities
      without any relation.
    - A dead id aborts the whole task.
  - **Remove relation:**
    - It deletes the newest relation between the two issues, whatever its type.
    - A missing relation, or no `related_issue`, gives a 500.
  - **Sub-issues POST:**
    - It logs `current_instance={"parent": <the sub-issue itself>}`.
    - It lets an issue become its own parent.
    - Its `IssueSerializer` output leaves out the annotation fields.
  - **Description-version cursors:** a malformed, zero or negative page size gives a 500.

## 4. Shared files changed

- `internal/api/issue_activity.go`: one extra `case` in the `issueActivity` type switch:

  ```go
  case "link.activity.created", "link.activity.updated", "link.activity.deleted",
      "issue_relation.activity.created", "issue_relation.activity.deleted":
      err = t.structureActivity(ctx) // issue_activity_structure.go
  ```

  The trackers live in `issue_activity_structure.go`:
  - `structureActivity`;
  - the `pyDict.strOr` and `pyDict.uuid` helpers;
  - `addFor` (an activity on another issue than the task's, for the relation's inverse side).

  Sub-issues reuse `trackParent` through `issue.activity.updated`.
- Reused without changes: `projectEntityPerm`, `allowProject`, `guestBlocked`, `modelUUIDs`, `issueBaseWhere`,
  `issueFrom` and the annotation SQL constants, `sanitizeOrderBy`, `userLite`/`imageURL`, `softdelete.Row`.

## 5. Unsure / follow-ups

- The real-network crawl is unrecorded by design (see section 3).
- `TestIssueBulk` fails on this branch only because its golden holds the recording date (`archived_at`).
  The lead fixed it on main with `AliasToday`. The goldens on this branch contain no dates.
