# File assets (PORTING.md section 17, plus the v2 issue attachments of section 7)

## 1. Endpoints ported and goldens

`AW` = `/api/assets/v2/workspaces/<slug>/`, `AP` = `AW` + `projects/<project_id>/`.

| Endpoint | Golden |
|---|---|
| `POST /api/assets/v2/user-assets/` | `asset_user` (54 steps) |
| `PATCH`, `DELETE /api/assets/v2/user-assets/<asset_id>/` | `asset_user` |
| `GET /api/assets/v2/static/<asset_id>/` (AllowAny) | `asset_user`, `asset_workspace`, `asset_project` |
| `POST AW/` (each entity_type case) | `asset_workspace` (124 steps) |
| `GET`, `PATCH`, `DELETE AW/<asset_id>/` | `asset_workspace` |
| `POST AW/restore/<asset_id>/` | `asset_workspace` |
| `GET AW/check/<asset_id>/` | `asset_workspace` |
| `POST AW/duplicate-assets/<asset_id>/` (with the per-asset 5/minute throttle) | `asset_workspace` |
| `GET AW/download/<asset_id>/` | `asset_workspace` |
| `POST AP/` | `asset_project` (86 steps) |
| `GET`, `PATCH AP/<pk>/` | `asset_project` |
| `POST AP/<entity_id>/bulk/` (each entity-type branch) | `asset_project` |
| `GET AP/download/<asset_id>/` | `asset_project` |
| Page duplicate: image-asset copy (`copy_s3_objects_of_description_and_assets`) | `asset_project` |
| `GET`, `POST AP/issues/<issue_id>/attachments/` | `asset_attachments` (68 steps) |
| `GET`, `PATCH`, `DELETE AP/issues/<issue_id>/attachments/<pk>/` | `asset_attachments` |
| `?expand=issue_attachments` on issue detail (the `asset` value) | `asset_attachments` |
| `DELETE /api/workspaces/file-assets/<workspace_id>/<asset_key>/` | `asset_legacy` (38 steps) |
| `POST /api/workspaces/file-assets/<workspace_id>/<asset_key>/restore/` | `asset_legacy` |
| `DELETE /api/users/file-assets/<asset_key>/` | `asset_legacy` |

What the scenarios cover:
- Roles: admin, member, guest and non-member, plus anonymous access.
- Validation:
  - bad entity_type and bad MIME type;
  - a size over FILE_SIZE_LIMIT, and a size that isn't an int (500, as in Django);
  - a non-dict body;
  - entity_identifier set to false, "", null or junk.
- Unknown ids and assets that aren't uploaded yet.
- Repeated logo and avatar PATCHes.
- Restore, check, duplicate (including the 429 on the 6th call) and download.
- Static: avatar, logo and cover; script-capable types come back as attachments.
- Bulk branches: cover, issue, comment, page and draft.
- The end-to-end flow: POST, upload to the returned form, PATCH, GET 302, then fetching the object from the bucket. The scenarios check the object's content, Content-Type and Content-Disposition.

Jobs:
- `get_asset_object_metadata`: an S3 HEAD whose result goes into `file_assets.storage_metadata`. It's a River job registered in `routes_asset.go` and covered by the goldens through `storage_metadata` and `last_modified_ok`.
- `delete_unuploaded_file_asset`: `deleteUnuploadedAssets(ctx, q, days)` plus a registered job (`unuploadedAssetsJob`). `UNUPLOADED_ASSET_DELETE_DAYS` defaults to 7.
  - Its unit test is `internal/api/asset_task_test.go`. It runs in a rolled-back transaction on the slot's test database and skips when that database is down.
  - **Not scheduled**: per the brief, the lead wires it into the periodic jobs. Django runs it daily at 02:00.

Unit tests:
- `internal/storage/post_test.go`: the policy round trip, expiry, tampered fields, extra fields, a wrong signature or secret, and size limits.

Files:
- `internal/storage/` (new package):
  - `storage.go`: the S3 client (aws-sdk-go-v2 `service/s3`, path-style). It offers PresignGet, Put, Head and Copy.
  - `post.go`: builds and checks the SigV4 POST policy.
- `internal/api/asset.go`: shared helpers, including:
  - entity types and MIME lists;
  - upload request parsing;
  - FileAsset inserts;
  - `asset_url`;
  - Content-Disposition and the redirect;
  - the confirm step and the metadata job.
- `internal/api/asset_user.go`: the user assets and static endpoints.
- `internal/api/asset_workspace.go`: the workspace and project endpoints, bulk, check, restore, duplicate and download.
- `internal/api/asset_attachment.go`: the issue attachments.
- `internal/api/asset_legacy.go`: the legacy endpoints, plus the route dispatcher (`assetSubroute`).
- `internal/api/asset_upload.go`: the upload proxy.
- `internal/api/asset_task.go`: the cleanup job and `copyEntityAssets`, which is copy_assets for page duplicates.
- `internal/api/issue_activity_asset.go`: the `attachment.activity.created/deleted` events.
- `contract/asset_test.go`.

## 2. Not ported

- Rows marked UNUSED in PORTING.md:
  - `GET`/`POST /api/workspaces/file-assets/...`
  - `GET`/`POST /api/users/file-assets/...`
  - `DELETE AP/<pk>/`
- The `(unused)` URL variants:
  - `GET`/`PATCH`/`DELETE AW/`
  - `POST AW/<asset_id>/`
  - `POST AP/<pk>/`
  - `PATCH`/`DELETE /api/assets/v2/user-assets/`
  - `POST` with a pk on attachments

  Their URLs exist, so a method the Go side doesn't route answers 405 with `Allow`, as for the other unused variants in DEVIATIONS.md.
- v1 `issue-attachments/` (UNUSED, section 7).

## 3. Deviations

Suggested DEVIATIONS.md rows:

| Area | Django | Go | Why |
|---|---|---|---|
| Upload protocol | Presigned S3 POST: `upload_data.url` is the bucket and the browser posts straight to S3 | The same response shape (`upload_data: {url, fields}`, `asset_id`, `asset_url`), but `upload_data.url` is `/api/assets/v2/upload/<asset_id>/` on the API. That endpoint takes the same multipart form, checks the signed policy the way S3 would, then does a `PutObject` and answers 204, or an S3 XML error (`EntityTooLarge`, `EntityTooSmall`, `AccessDenied`, `SignatureDoesNotMatch`, `MalformedPOSTRequest`). It also checks that the policy's key belongs to that asset. | Production storage is Cloudflare R2, which has no presigned POST. The web client is unchanged: it posts `fields` + `file` to `url`. |
| Upload size and memory | S3 streams the upload | Up to 8 MiB is held in memory and larger uploads spool to a temp file. The body is capped at FILE_SIZE_LIMIT + 1 MiB. | PutObject needs a seekable, known-length body to sign |
| Storage not configured | boto3 fails while presigning (500) | The server starts without storage (a warning is logged). Asset endpoints that need the bucket answer 503 "File storage is not configured." | Running without storage is allowed by design |
| Page duplicate: image assets | (row already in DEVIATIONS.md) | Now ported: copies the assets, rewrites the `src` ids, marks them uploaded | **Remove the existing row** |
| Reference `USE_MINIO` | With `USE_MINIO=1`, Django signs URLs for the request's host | The reference runs with `USE_MINIO=0` and `AWS_S3_ENDPOINT_URL=http://minio:9000`. Recording rewrites that host to the slot's MinIO port and keeps the signed Host header. | With `USE_MINIO=1` the reference container's own `copy_object` can't reach the browser-facing host, so duplicates 500. This affects the test setup only. |

Golden masks, each commented in `contract/asset_test.go`:
- `upload_data.url`, `policy`, `x-amz-credential`, `x-amz-date` and `x-amz-signature` are masked. They point at a different URL by design, and in Django they vary with time.
- Keys and asset fields containing `uuid4().hex` are normalized. Separate `KEY` check steps assert that the masked values equal the stored key.
- Presigned GET query strings: the path and the `response-content-disposition` stay checked; the signature is masked.

## 4. Shared-file changes

- `docker-compose.dev.yml`:
  - New `minio` service under `profiles: ["s3"]`, so it's off by default. It uses the `bitnamilegacy/minio:2025.7.23-debian-12-r5` image: Docker Hub's `minio/minio` repository is gone and quay.io needs auth.
  - The bucket `uploads` is created at startup.
  - The healthcheck signs in with `mc` and stats the bucket. An anonymous probe gets 403 whether the bucket exists or not, which made tests race the bucket creation.
  - The reference gets `USE_MINIO=0`, `AWS_S3_ENDPOINT_URL=http://minio:9000`, the keys, `AWS_S3_BUCKET_NAME=uploads` and `AWS_REGION=us-east-1`.
  - The reference does not depend on minio.
- `scripts/devstack.sh`:
  - `PLANE_DEV_MINIO_PORT=59000+slot*10`.
  - `PLANE_DEV_S3=1` adds `--profile s3`; documented in the header.
  - `down` always includes the profile.
  - `env` prints `PLANE_DEV_S3` when it's set.
- `internal/config/config.go`: new `Storage` struct, plus `UnuploadedAssetDeleteDays`. The fields are in their own block, so existing lines didn't need realigning.
- `internal/api/api.go`: a `storage *storage.Client` field. It's nil without storage, and `registerAssetJobs` builds it.
- `internal/api/issue_activity.go`: two `case` lines dispatching the `attachment.activity.*` events to `issue_activity_asset.go`.
- `internal/api/issue.go`: a comment only. `issue_attachments[].asset` is the stored key, which is also what `S3Storage.url` returns, so the golden matches.
- `internal/api/page_task.go`: `copyPageDescription` now copies the assets (via `copyEntityAssets`) and rewrites `src`. The "not ported" warning is gone.
- `contract/env.go`, `contract/target.go`: the storage target comes from env. Asset scenarios `t.Skip` when none is set; everything else is unaffected.
- `go.mod`, `go.sum`: `github.com/aws/aws-sdk-go-v2/service/s3` v1.113.0 and its deps (aws-sdk-go-v2 core, smithy-go; first-party only).
- Routing:
  - `/api/workspaces/file-assets/<uuid>/<key>/` overlaps every `workspaces/<slug>/<name>/<id>/` route in ServeMux.
  - `AP/<uuid>/bulk/` overlaps `AP/download/<uuid>/`.
  - These are served from all-wildcard patterns (`wsPrefix+"{a}/{b}/"`, `.../{a}/{b}/{c}/`, `AP+"{a}/{b}/"`). Every other route outranks those patterns. They resolve the view themselves, and anything else gets Django's 404, or 401 / 405 + Allow. Later areas adding 4-segment workspace routes are unaffected, since literal segments are more specific.

## 5. Configuration (for README "Configuration"; not edited, per the PLAYBOOK)

| Variable | Default | Meaning |
|---|---|---|
| `AWS_S3_ENDPOINT_URL` | — | S3-compatible endpoint. For R2: `https://<account_id>.r2.cloudflarestorage.com` |
| `AWS_ACCESS_KEY_ID` | — | Access key (R2: an API token's access key ID) |
| `AWS_SECRET_ACCESS_KEY` | — | Secret key; it also signs the upload policies |
| `AWS_S3_BUCKET_NAME` | `uploads` | Bucket |
| `AWS_REGION` | `auto` | Signing region (`auto` for R2) |
| `SIGNED_URL_EXPIRATION` | `3600` | Lifetime of presigned GET URLs and upload policies, in seconds |
| `UNUPLOADED_ASSET_DELETE_DAYS` | `7` | Age after which never-uploaded assets are soft-deleted |

The server starts without the first four. Asset endpoints then answer 503.

**Cloudflare R2:**
1. Create a bucket.
2. Create an R2 API token with Object Read & Write on that bucket.
3. Set the endpoint, keys and bucket name, with `AWS_REGION=auto`.

Addressing is path-style, and request checksums are sent only when required (R2 rejects some of the SDK defaults). Uploads go through the API, so the bucket needs no CORS rule for POST. Browsers follow the presigned GET redirects straight to R2; that needs no CORS for `<img>` and downloads.

**MinIO** is only an optional local stand-in. Start it with `PLANE_DEV_S3=1 scripts/devstack.sh N up`, then `export PLANE_DEV_S3=1` for go test. To check the Go side against a real bucket (e.g. an R2 test bucket), set `CONTRACT_S3_ENDPOINT`, `CONTRACT_S3_ACCESS_KEY`, `CONTRACT_S3_SECRET_KEY`, `CONTRACT_S3_BUCKET` (default `uploads`) and `CONTRACT_S3_REGION` (default `auto`). Recording always needs the MinIO profile, because the Django reference uses it.

Verification, slot 3:
- Without MinIO: the full suite is green, and the five asset scenarios skip with a message.
- With `PLANE_DEV_S3=1`: the full suite is green, including the five asset scenarios. They and the page scenarios also passed at `-count=5`.

## 6. Open questions

- I haven't run the Go side against real R2. The SDK settings (`UsePathStyle`, `RequestChecksumCalculationWhenRequired`, region `auto`) follow Cloudflare's guidance, and the contract env vars above allow that check.
- The upload proxy sends the file bytes through the API server. If large attachments become common, a presigned PUT would avoid that, but the web client would need changing.
- The cleanup job needs a daily schedule (02:00 in Django), which the lead wires at merge.
- In the PORTING.md section 7 row for issue detail, the batch 8 note ("revisit with storage URLs in section 17") can go: the stored key is what Django returns.
