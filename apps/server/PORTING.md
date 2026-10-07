# Porting checklist: Django `apps/api` → Go `apps/server`

Source snapshot: commit `1682c5bac` (branch `preview`), 2026-10-07. The tables were extracted with an AST pass over
`apps/api/plane/app/urls/*.py` and `plane/authentication/urls.py`, resolved to view methods. Callers come from a scan
of every `/api/` and `/auth/` string in `apps/web`, `apps/live/src` and `packages/{utils,editor,shared-state,services}`.
Hand-verified notes are added on top.

This document covers every **kept** endpoint that is not yet ported. CUT features and the batch-1 endpoints are listed
only in [Appendix A](#appendix-a-excluded-endpoints).

> Status: sections 1 and 2 are ported (batches 3 and 4), section 3 (batch 5), section 4 (batch 6), section 5 (batch 7) and section 6 (batch 8), except the unused rows noted in DEVIATIONS.md; their rows are ticked.

## How to read the tables

- **One row = one (view method, HTTP method).** Some DRF `APIView` classes are mounted on several paths and accept
  every method they define on each of them. Those path variants are merged into one row. A variant with no caller is
  marked `(unused)`.
- **URL prefixes:** `W` = `/api/workspaces/<str:slug>`, `P` = `W/projects/<project_id>`,
  `AW` = `/api/assets/v2/workspaces/<str:slug>`, `AP` = `AW/projects/<project_id>`. `<name>` is a `uuid` converter
  (a non-UUID returns 404 in Django). `<str:name>` is a string. Unconverted `<uidb64>`/`<token>`/`<estimate_point_id>`
  segments are strings too.
- **View · file:line:** paths are relative to `apps/api/plane/app/views/`. `auth:` means
  `apps/api/plane/authentication/views/`. A `(DRF default)` row means the action is the stock `ModelViewSet` mixin. The
  logic then lives in `get_queryset` / `perform_create` (listed) plus `serializer_class`.
- **Serializer(s):** serializer classes referenced in the method body, used for input validation and/or the response.
  `— (dict/values)` means the response is built from `.values()` or a dict. `(+X)` marks serializers reached through
  helper methods.
- **Permission:** `AP[A,M,G]/WS` = `@allow_permission([ADMIN, MEMBER, GUEST], level="WORKSPACE")`, and `/PROJ` = project
  level (the default). `+creator(Model)` = `creator=True, model=Model`. `AP[—]` = empty role list, so only the creator
  passes. Other names are DRF `permission_classes`, and `IsAuthenticated` is omitted when a decorator is present. See
  [Shared building blocks](#1-shared-building-blocks-port-once).
- **Side effects:** names are Celery tasks invoked with `.delay()` (directly or through helpers in the same module).
  `issue_activity(+notif)` = called with `notification=True`, so it fans out to the `notifications` task.
  **†** = webhook-only task (`model_activity` / `webhook_activity`), which is a no-op once webhooks are cut, so
  **drop it in Go**. `invalidate_cache` / `cache_response` = Redis response cache (see building blocks). "writes on GET"
  = the GET creates rows (lazy defaults).
- **Caller:** `web` = `apps/web`, `live` = `apps/live` (collab server), `editor` = `packages/utils/src/editor` (used by
  web and live to build asset URLs). `**UNUSED**` = no caller found. "dead web service fn" = the URL exists only inside
  a service method that nothing calls. `@plane/services` is imported by `apps/admin`/`apps/space` only, not by
  `apps/web`, so matches found only there count as unused.
- **Cx:** S = trivial CRUD/serializer. M = some business rules, several queries, or a task. L = complex annotated
  querysets, grouping/pagination, or heavy side effects.
- Checkbox `☐` = ported + golden recorded + green.

## Summary

| Domain | Endpoints | Used by web/live | Unused | L (used) | M | S |
|---|---|---|---|---|---|---|
| 1. Auth (remaining) | 7 | 7 | 0 | 0 | 4 | 3 |
| 2. Users / profile / settings / onboarding | 13 | 9 | 4 | 1 (1) | 2 | 10 |
| 3. Workspaces, members, invitations | 35 | 22 | 13 | 1 (1) | 10 | 24 |
| 4. Projects, members, invitations, join | 32 | 19 | 13 | 4 (4) | 11 | 17 |
| 5. States & labels | 15 | 12 | 3 | 0 | 0 | 15 |
| 6. Issues (core) | 19 | 15 | 4 | 9 (8) | 7 | 3 |
| 7. Issue sub-resources | 39 | 27 | 12 | 2 (2) | 22 | 15 |
| 8. Cycles | 22 | 18 | 4 | 8 (8) | 9 | 5 |
| 9. Modules | 25 | 17 | 8 | 8 (6) | 4 | 13 |
| 10. Estimates | 10 | 6 | 4 | 0 | 3 | 7 |
| 11. Saved views | 13 | 11 | 2 | 1 (1) | 2 | 10 |
| 12. Pages | 15 | 14 | 1 | 1 (1) | 9 | 5 |
| 13. Stickies & home | 12 | 10 | 2 | 0 | 2 | 10 |
| 14. Notifications | 12 | 11 | 1 | 1 (1) | 1 | 10 |
| 15. Favorites & recents | 20 | 15 | 5 | 0 | 3 | 17 |
| 16. Search | 3 | 3 | 0 | 1 (1) | 2 | 0 |
| 17. File assets | 25 | 20 | 5 | 0 | 12 | 13 |
| 18. Misc (timezones, user activity/profile) | 10 | 6 | 4 | 4 (3) | 5 | 1 |
| **Total** | **327** | **242** | **85** | **41 (37)** | **108** | **178** |

Notable findings:

- **85 endpoints have no caller** and can be deferred: all `PUT` (DRF `update`) routes, legacy v1 file-asset routes,
  `IssueAttachmentEndpoint` (v1), workspace themes, project invitations/join, `ModuleIssueViewSet.list`, and others.
  Django would answer them, while Go answers 404, so record any you skip in `DEVIATIONS.md`.
- Module and cycle issue **lists** in the web go through `IssueViewSet.list` (`P/issues/?module=…`). `ModuleIssueViewSet.list`
  is dead (`getModuleIssues` is never called). `CycleIssueViewSet.list` is still used by the cycle store.
- Kept responses still read **CUT tables**. `ProjectViewSet.list` returns `intake_count` (from `intake_issues`).
  `ProjectViewSet.get_queryset` annotates `anchor` from `deploy_boards`. `ProjectViewSet.partial_update` creates an
  `Intake` row when `intake_view=true`. `GlobalSearchEndpoint` has an `intake` entity. Keep those tables in the schema,
  or hard-code `0` / `null` / `[]` and note it in `DEVIATIONS.md`.
- `model_activity` (called from project, issue, cycle, module and comment writes) and `webhook_activity` only feed
  webhooks, so drop them.

## Endpoint checklist (dependency order)

### 1. Auth (remaining)

7 endpoints · 0 L / 4 M / 3 S · 0 unused by web/live

All are form-POST + 302 redirect flows (Django `View`) except magic-generate / forgot-password (DRF `APIView`, JSON). They reuse the sign-in/sign-up post-login workflow already in Go (`internal/api/login.go`). Magic codes live in Redis (`magic_<email>`, attempt counter `…:verify_attempts`); reset tokens must be Django `PasswordResetTokenGenerator`-compatible.

| ☑ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☑ | POST | `/auth/magic-generate/` | `MagicGenerateEndpoint.post` auth:app/magic.py:41 | — (dict/values) | AllowAny | magic_link. Redis key `magic_<email>` (token+attempts, 10 min TTL); creates nothing in DB | web | M |
| ☑ | POST | `/auth/magic-sign-in/` | `MagicSignInEndpoint.post` auth:app/magic.py:66 | — (dict/values) | none (Django View) | form POST → 302 redirect; validates Redis magic token; `user_login` session; shares post-login workflow with sign-in (accept invites) | web | M |
| ☑ | POST | `/auth/magic-sign-up/` | `MagicSignUpEndpoint.post` auth:app/magic.py:138 | — (dict/values) | none (Django View) | form POST → 302; creates User + Profile (+UserNotificationPreference via post_save); session login | web | M |
| ☑ | POST | `/auth/forgot-password/` | `ForgotPasswordEndpoint.post` auth:app/password_management.py:50 | — (dict/values) | AllowAny | forgot_password. Django `PasswordResetTokenGenerator` token + uidb64 in email link | web | S |
| ☑ | POST | `/auth/reset-password/<uidb64>/<token>/` | `ResetPasswordEndpoint.post` auth:app/password_management.py:100 | — (dict/values) | none (Django View) | form POST → 302; verifies Django reset token (must match Django's HMAC algorithm) | web | M |
| ☑ | POST | `/auth/change-password/` | `ChangePasswordEndpoint.post` auth:common.py:48 | — (dict/values) | IsAuthenticated (DRF default) | password strength check (zxcvbn); re-login session | web | S |
| ☑ | POST | `/auth/set-password/` | `SetUserPasswordEndpoint.post` auth:common.py:101 | UserSerializer | IsAuthenticated (DRF default) | invalidate_cache: `/api/users/me/`. only when `is_password_autoset`; re-login session | web | S |

### 2. Users / profile / settings / onboarding

13 endpoints · 1 L / 2 M / 10 S · 4 unused by web/live

`GET /api/users/me/` is already ported. `/users/me/settings/` and `/users/me/profile/` are boot calls (root layout).

| ☑ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☑ | PATCH | `/api/users/me/` | `UserEndpoint.partial_update` user/base.py:92 | UserSerializer | IsAuthenticated | `super().partial_update` with `get_object()` = request.user | web | S |
| ☑ | DELETE | `/api/users/me/` | `UserEndpoint.deactivate` user/base.py:252 | — (dict/values) | IsAuthenticated | user_deactivation_email. 400 for instance admins and sole admins of shared projects/workspaces; bulk-deactivates ProjectMember/WorkspaceMember; deletes WorkspaceMemberInvite + all Sessions; resets Profile; logout | web | L |
| ☑ | GET | `/api/users/session/` | `UserSessionEndpoint.get` user/base.py:354 | UserMeSerializer | AllowAny | — | **UNUSED** | S |
| ☑ | GET | `/api/users/me/settings/` | `UserEndpoint.retrieve_user_settings` user/base.py:83 | UserMeSettingsSerializer | IsAuthenticated | cache_control(private, 12s); `{id,email,workspace:{last_workspace_id/slug, invites count, fallback}}`; boot call | web | S |
| ☑ | POST | `/api/users/me/email/generate-code/` | `UserEndpoint.generate_email_verification_code` user/base.py:137 | — (dict/values) | IsAuthenticated | send_email_update_magic_code. code in Django cache (Redis); EmailVerificationThrottle 3/h | web | M |
| ☑ | PATCH | `/api/users/me/email/` | `UserEndpoint.update_email` user/base.py:176 | UserMeSerializer | IsAuthenticated | send_email_update_confirmation. verifies code from Django cache (Redis); logs user out | web | M |
| ☑ | GET | `/api/users/me/profile/` | `ProfileEndpoint.get` user/base.py:419 | ProfileSerializer | IsAuthenticated | — | web | S |
| ☑ | PATCH | `/api/users/me/profile/` | `ProfileEndpoint.patch` user/base.py:424 | ProfileSerializer | IsAuthenticated | — | web | S |
| ☑ | GET | `/api/users/me/accounts/`<br>`/api/users/me/accounts/<pk>/` | `AccountEndpoint.get` user/base.py:400 | AccountSerializer | IsAuthenticated | — | **UNUSED** (dead web service fn `getCurrentUserAccounts`) | S |
| ☑ | DELETE | `/api/users/me/accounts/`<br>`/api/users/me/accounts/<pk>/` | `AccountEndpoint.delete` user/base.py:410 | — (dict/values) | IsAuthenticated | — | **UNUSED** | S |
| ☑ | GET | `/api/users/me/instance-admin/` | `UserEndpoint.retrieve_instance_admin` user/base.py:87 | — (dict/values) | IsAuthenticated | — | **UNUSED** (dead web service fn `currentUserInstanceAdminStatus`) | S |
| ☑ | PATCH | `/api/users/me/onboard/` | `UpdateUserOnBoardedEndpoint.patch` user/base.py:366 | — (dict/values) | IsAuthenticated | — | web | S |
| ☑ | PATCH | `/api/users/me/tour-completed/` | `UpdateUserTourCompletedEndpoint.patch` user/base.py:374 | — (dict/values) | IsAuthenticated | — | web | S |

### 3. Workspaces, members, invitations

35 endpoints · 1 L / 10 M / 24 S · 13 unused by web/live

Boot calls: `/users/me/workspaces/`, `W/workspace-members/me/`, `W/members/`, `W/sidebar-preferences/`, `W/user-properties/`, `/users/me/workspaces/<slug>/project-roles/` (section 4). `WorkSpaceViewSet.create` enqueues `workspace_seed` (demo data) — not kept: see DEVIATIONS.md.

| ☑ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☑ | GET | `/api/users/me/workspaces/` | `UserWorkSpacesEndpoint.get` workspace/base.py:180 | WorkSpaceSerializer | IsAuthenticated | annotates total_members, role; boot call | web | M |
| ☑ | GET | `/api/workspace-slug-check/` | `WorkSpaceAvailabilityCheckEndpoint.get` workspace/base.py:215 | — (dict/values) | IsAuthenticated | — | web | S |
| ☑ | GET | `/api/workspaces/` | `WorkSpaceViewSet.list` workspace/base.py:153 | WorkSpaceSerializer | AP[A,M,G]/WS · WorkSpaceBasePermission | — | **UNUSED** | S |
| ☑ | POST | `/api/workspaces/` | `WorkSpaceViewSet.create` workspace/base.py:81 | WorkSpaceSerializer | WorkSpaceBasePermission | workspace_seed. creates Workspace + WorkspaceMember(admin) + `workspace_seed` task (seeds demo project/states/labels/issues/cycles/modules/pages/views via bot user) — decide whether to keep | web | L |
| ☑ | GET | `W/` | `WorkSpaceViewSet.retrieve` (DRF default) class workspace/base.py:53; get_queryset workspace/base.py:63 | WorkSpaceSerializer | WorkSpaceBasePermission | — | **UNUSED** (dead web service fn `getWorkspace`) | M |
| ☑ | PUT | `W/` | `WorkSpaceViewSet.update` (DRF default) class workspace/base.py:53; get_queryset workspace/base.py:63 | WorkSpaceSerializer | WorkSpaceBasePermission | — | **UNUSED** | M |
| ☑ | PATCH | `W/` | `WorkSpaceViewSet.partial_update` workspace/base.py:157 | WorkSpaceSerializer | AP[A]/WS · WorkSpaceBasePermission | — | web | S |
| ☑ | DELETE | `W/` | `WorkSpaceViewSet.destroy` workspace/base.py:168 | WorkSpaceSerializer | AP[A]/WS · WorkSpaceBasePermission | soft delete + slug suffixed with `__<epoch>`; cascade via soft_delete_related_objects | web | S |
| ☑ | GET | `W/invitations/` | `WorkspaceInvitationsViewset.list` (DRF default) class workspace/invite.py:36; get_queryset workspace/invite.py:44 | WorkSpaceMemberInviteSerializer | WorkSpaceAdminPermission | — | web | S |
| ☑ | POST | `W/invitations/` | `WorkspaceInvitationsViewset.create` workspace/invite.py:52 | WorkSpaceMemberSerializer | WorkSpaceAdminPermission | workspace_invitation. WorkspaceMemberInvite rows + email per invite | web | M |
| ☑ | DELETE | `W/invitations/<pk>/` | `WorkspaceInvitationsViewset.destroy` workspace/invite.py:130 | — (dict/values) | WorkSpaceAdminPermission | — | web | S |
| ☑ | GET | `W/invitations/<pk>/` | `WorkspaceInvitationsViewset.retrieve` (DRF default) class workspace/invite.py:36; get_queryset workspace/invite.py:44 | WorkSpaceMemberInviteSerializer | WorkSpaceAdminPermission | — | **UNUSED** | S |
| ☑ | PATCH | `W/invitations/<pk>/` | `WorkspaceInvitationsViewset.partial_update` (DRF default) class workspace/invite.py:36; get_queryset workspace/invite.py:44 | WorkSpaceMemberInviteSerializer | WorkSpaceAdminPermission | — | web | S |
| ☑ | GET | `/api/users/me/workspaces/invitations/` | `UserWorkspaceInvitationsViewSet.list` (DRF default) class workspace/invite.py:236; get_queryset workspace/invite.py:240 | WorkSpaceMemberInviteSerializer | IsAuthenticated | — | web | S |
| ☑ | POST | `/api/users/me/workspaces/invitations/` | `UserWorkspaceInvitationsViewSet.create` workspace/invite.py:247 | — (dict/values) | IsAuthenticated | invalidate_cache: `/api/workspaces/`, `/api/users/me/workspaces/`; invalidate_cache_directly. bulk-accept invites → WorkspaceMember rows | web | M |
| ☑ | GET | `W/invitations/<pk>/join/` | `WorkspaceJoinEndpoint.get` workspace/invite.py:227 | WorkSpaceMemberInvitePublicSerializer | AllowAny | — | web | S |
| ☑ | POST | `W/invitations/<pk>/join/` | `WorkspaceJoinEndpoint.post` workspace/invite.py:149 | — (dict/values) | AllowAny | invalidate_cache: `/api/workspaces/`, `/api/users/me/workspaces/`, `/api/workspaces/:slug/members/`, `/api/users/me/settings/`. token-checked accept/reject; creates or reactivates WorkspaceMember; sets Profile.last_workspace_id | web | M |
| ☑ | GET | `W/members/` | `WorkSpaceMemberViewSet.list` workspace/member.py:46 | WorkspaceMemberAdminSerializer, WorkSpaceMemberSerializer | AP[A,M,G]/WS | — | web | S |
| ☑ | GET | `W/project-members/` | `WorkspaceProjectMemberEndpoint.get` workspace/member.py:243 | ProjectMemberRoleSerializer | WorkspaceEntityPermission | — | **UNUSED** | S |
| ☑ | PATCH | `W/members/<pk>/` | `WorkSpaceMemberViewSet.partial_update` workspace/member.py:77 | WorkSpaceMemberSerializer | AP[A]/WS | — | web | S |
| ☑ | DELETE | `W/members/<pk>/` | `WorkSpaceMemberViewSet.destroy` workspace/member.py:99 | — (dict/values) | AP[A]/WS | guards (self, higher role, sole project admin); deactivates WorkspaceMember + that user's ProjectMember rows | web | M |
| ☑ | GET | `W/members/<pk>/` | `WorkSpaceMemberViewSet.retrieve` workspace/member.py:58 | WorkspaceMemberAdminSerializer, WorkSpaceMemberSerializer | AP[A,M,G]/WS | — | **UNUSED** | S |
| ☑ | POST | `W/members/leave/` | `WorkSpaceMemberViewSet.leave` workspace/member.py:161 | — (dict/values) | AP[A,M,G]/WS | invalidate_cache: `/api/workspaces/:slug/members/`, `/api/users/me/settings/`, `api/users/me/workspaces/`. guards (last ws admin, sole project admin); deactivates memberships | web | M |
| ☐ | GET | `/api/users/last-visited-workspace/` | `UserLastProjectWithWorkspaceEndpoint.get` workspace/user.py:69 | WorkSpaceSerializer, ProjectMemberSerializer | IsAuthenticated | — | **UNUSED** (dead web service fn `getLastActiveWorkspaceAndProjects`) | S |
| ☑ | GET | `W/workspace-members/me/` | `WorkspaceMemberUserEndpoint.get` workspace/member.py:220 | WorkspaceMemberMeSerializer | IsAuthenticated | boot call (workspace wrapper) | web | M |
| ☑ | POST | `W/workspace-views/` | `WorkspaceMemberUserViewsEndpoint.post` workspace/member.py:209 | — (dict/values) | IsAuthenticated | — | **UNUSED** (dead web service fn `updateWorkspaceView`) | S |
| ☐ | GET | `W/workspace-themes/` | `WorkspaceThemeViewSet.list` (DRF default) class workspace/base.py:322; get_queryset workspace/base.py:327 | WorkspaceThemeSerializer | WorkSpaceAdminPermission | — | **UNUSED** | S |
| ☐ | POST | `W/workspace-themes/` | `WorkspaceThemeViewSet.create` workspace/base.py:330 | WorkspaceThemeSerializer | WorkSpaceAdminPermission | — | **UNUSED** | S |
| ☐ | GET | `W/workspace-themes/<pk>/` | `WorkspaceThemeViewSet.retrieve` (DRF default) class workspace/base.py:322; get_queryset workspace/base.py:327 | WorkspaceThemeSerializer | WorkSpaceAdminPermission | — | **UNUSED** | S |
| ☐ | PATCH | `W/workspace-themes/<pk>/` | `WorkspaceThemeViewSet.partial_update` (DRF default) class workspace/base.py:322; get_queryset workspace/base.py:327 | WorkspaceThemeSerializer | WorkSpaceAdminPermission | — | **UNUSED** | S |
| ☐ | DELETE | `W/workspace-themes/<pk>/` | `WorkspaceThemeViewSet.destroy` (DRF default) class workspace/base.py:322; get_queryset workspace/base.py:327 | WorkspaceThemeSerializer | WorkSpaceAdminPermission | — | **UNUSED** | S |
| ☑ | GET | `W/user-properties/` | `WorkspaceUserPropertiesEndpoint.get` workspace/user.py:269 | WorkspaceUserPropertiesSerializer | WorkspaceViewerPermission | writes on GET | web | S |
| ☑ | PATCH | `W/user-properties/` | `WorkspaceUserPropertiesEndpoint.patch` workspace/user.py:255 | WorkspaceUserPropertiesSerializer | WorkspaceViewerPermission | — | web | S |
| ☑ | GET | `W/sidebar-preferences/` | `WorkspaceUserPreferenceViewSet.get` workspace/user_preference.py:26 | — (dict/values) | AP[A,M,G]/WS | writes on GET: bulk_create missing sidebar keys | web | M |
| ☑ | PATCH | `W/sidebar-preferences/` | `WorkspaceUserPreferenceViewSet.patch` workspace/user_preference.py:82 | — (dict/values) | AP[A,M,G]/WS | — | web | S |

### 4. Projects, members, invitations, join

32 endpoints · 4 L / 11 M / 17 S · 13 unused by web/live

Boot calls: `W/projects/` (lite list), `P/` detail, `P/project-members/me/`, `P/user-properties/`, `P/members/`. Project creation seeds DEFAULT_STATES (`DEFAULT_STATES` in `plane/db/models/state.py:24`). ProjectMember.save() creates ProjectUserProperty — but several endpoints use `bulk_create`, which bypasses save() and creates the property rows explicitly.

| ☑ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☑ | GET | `P/user-properties/` | `ProjectUserDisplayPropertyEndpoint.get` issue/base.py:767 | ProjectUserPropertySerializer | AP[A,M,G]/PROJ | writes on GET | web | S |
| ☑ | PATCH | `P/user-properties/` | `ProjectUserDisplayPropertyEndpoint.patch` issue/base.py:745 | ProjectUserPropertySerializer | AP[A,M,G]/PROJ | — | web | S |
| ☑ | GET | `W/projects/` | `ProjectViewSet.list` project/base.py:146 | — (dict/values) | AP[A,M,G]/WS | boot call; values() incl. member_role, `intake_count` (reads intake_issues although intake is CUT), sort_order subquery (ProjectUserProperty); guests see joined projects, members joined + network=2 | web | L |
| ☑ | POST | `W/projects/` | `ProjectViewSet.create` project/base.py:258 | ProjectSerializer, ProjectListSerializer | AP[A,M]/WS | model_activity†. creates Project + ProjectMember(admin, lead) + 5 DEFAULT_STATES; ProjectMember.save creates ProjectUserProperty | web | L |
| ☑ | GET | `W/projects/details/` | `ProjectViewSet.list_detail` project/base.py:102 | ProjectListSerializer | AP[A,M,G]/WS | get_queryset annotations (is_favorite, members_list prefetch, `anchor` from deploy_boards [CUT table], sort_order); paginated only when both `per_page` and `cursor` given | web | L |
| ☑ | GET | `W/projects/<pk>/` | `ProjectViewSet.retrieve` project/base.py:226 | ProjectListSerializer | AP[A,M,G]/WS | recent_visited_task | web | M |
| ☐ | PUT | `W/projects/<pk>/` | `ProjectViewSet.update` (DRF default) class project/base.py:47; get_queryset project/base.py:53 | ProjectListSerializer | IsAuthenticated | — | **UNUSED** | M |
| ☑ | PATCH | `W/projects/<pk>/` | `ProjectViewSet.partial_update` project/base.py:314 | ProjectSerializer, ProjectListSerializer | IsAuthenticated | model_activity†. inline perm: workspace admin or project admin; archived → 400; creates default Intake row when intake_view=true (intake CUT) | web | M |
| ☑ | DELETE | `W/projects/<pk>/` | `ProjectViewSet.destroy` project/base.py:382 | — (dict/values) | IsAuthenticated | webhook_activity†. inline perm (ws admin or project admin); soft delete cascade; `webhook_activity`* only | web | M |
| ☑ | GET | `W/project-identifiers/` | `ProjectIdentifierEndpoint.get` project/base.py:446 | — (dict/values) | AP[A,M]/WS | — | web | S |
| ☐ | DELETE | `W/project-identifiers/` | `ProjectIdentifierEndpoint.delete` project/base.py:457 | — (dict/values) | AP[A,M]/WS | — | **UNUSED** | S |
| ☐ | GET | `P/invitations/` | `ProjectInvitationsViewset.list` (DRF default) class project/invite.py:40; get_queryset project/invite.py:46 | ProjectMemberInviteSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | POST | `P/invitations/` | `ProjectInvitationsViewset.create` project/invite.py:57 | — (dict/values) | AP[A]/PROJ | project_invitations | **UNUSED** | M |
| ☐ | GET | `P/invitations/<pk>/` | `ProjectInvitationsViewset.retrieve` (DRF default) class project/invite.py:40; get_queryset project/invite.py:46 | ProjectMemberInviteSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | DELETE | `P/invitations/<pk>/` | `ProjectInvitationsViewset.destroy` (DRF default) class project/invite.py:40; get_queryset project/invite.py:46 | ProjectMemberInviteSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | GET | `/api/users/me/workspaces/<str:slug>/projects/invitations/` | `UserProjectInvitationsViewset.list` (DRF default) class project/invite.py:119; get_queryset project/invite.py:123 | ProjectMemberInviteSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☑ | POST | `/api/users/me/workspaces/<str:slug>/projects/invitations/` | `UserProjectInvitationsViewset.create` project/invite.py:132 | — (dict/values) | AP[A,M]/WS | self-join projects: reactivates + bulk_create ProjectMember and ProjectUserProperty | web | M |
| ☑ | GET | `/api/users/me/workspaces/<str:slug>/project-roles/` | `UserProjectRolesEndpoint.get` project/member.py:369 | — (dict/values) | WorkspaceUserPermission | boot call; returns {project_id: role} | web | S |
| ☐ | GET | `P/join/<pk>/` | `ProjectJoinEndpoint.get` project/invite.py:292 | ProjectMemberInvitePublicSerializer | AllowAny | — | **UNUSED** | S |
| ☐ | POST | `P/join/<pk>/` | `ProjectJoinEndpoint.post` project/invite.py:195 | — (dict/values) | AllowAny | token-checked accept; creates WorkspaceMember/ProjectMember | **UNUSED** | M |
| ☑ | GET | `P/members/` | `ProjectMemberViewSet.list` project/member.py:157 | ProjectMemberRoleSerializer | AP[A,M,G]/PROJ | — | web | S |
| ☑ | POST | `P/members/` | `ProjectMemberViewSet.create` project/member.py:47 | ProjectMemberRoleSerializer | AP[A]/PROJ | project_add_user_email. validates role vs workspace role; reactivates existing; bulk_create ProjectMember + ProjectUserProperty (bulk_create bypasses ProjectMember.save) with sort_order calc; email per member | web | L |
| ☑ | GET | `P/members/<pk>/` | `ProjectMemberViewSet.retrieve` project/member.py:172 | ProjectMemberAdminSerializer, ProjectMemberRoleSerializer | AP[A,M,G]/PROJ | — | **UNUSED** (dead web service fn `getProjectMember`) | M |
| ☑ | PATCH | `P/members/<pk>/` | `ProjectMemberViewSet.partial_update` project/member.py:206 | ProjectMemberSerializer | AP[A,M,G]/PROJ | inline role rules (can't exceed ws role / own role) | web | M |
| ☑ | DELETE | `P/members/<pk>/` | `ProjectMemberViewSet.destroy` project/member.py:291 | — (dict/values) | AP[A]/PROJ | can't remove self or a higher role; sets is_active=False | web | M |
| ☑ | POST | `P/members/leave/` | `ProjectMemberViewSet.leave` project/member.py:324 | — (dict/values) | AP[A,M,G]/PROJ | — | web | M |
| ☐ | POST | `P/project-views/` | `ProjectUserViewsEndpoint.post` project/base.py:475 | — (dict/values) | IsAuthenticated | — | **UNUSED** | S |
| ☑ | GET | `P/project-members/me/` | `ProjectMemberUserEndpoint.get` project/member.py:353 | ProjectMemberSerializer | IsAuthenticated | — | web | S |
| ☑ | POST | `P/archive/` | `ProjectArchiveUnarchiveEndpoint.post` project/base.py:429 | — (dict/values) | AP[A,M]/PROJ | sets archived_at; removes favorites | web | S |
| ☑ | DELETE | `P/archive/` | `ProjectArchiveUnarchiveEndpoint.delete` project/base.py:437 | — (dict/values) | AP[A,M]/PROJ | — | web | S |
| ☐ | GET | `P/preferences/member/<member_id>/` | `ProjectMemberPreferenceEndpoint.get` project/member.py:403 | ProjectMemberPreferenceSerializer | AP[A,M,G]/PROJ | — | **UNUSED** | S |
| ☐ | PATCH | `P/preferences/member/<member_id>/` | `ProjectMemberPreferenceEndpoint.patch` project/member.py:391 | ProjectMemberPreferenceSerializer | AP[A,M,G]/PROJ | — | **UNUSED** | S |

### 5. States & labels

15 endpoints · 0 L / 0 M / 15 S · 3 unused by web/live

Port before issues: `State.objects` excludes triage states (StateManager), `State.save()` sets slug + sequence (+15000), `Label.save()` sets sort_order (+10000). Workspace-level `W/states/` and `W/labels/` are boot calls; `W/labels/` and `W/estimates/` are Redis-cached 2 h with explicit invalidation from project label/estimate writes.

| ☑ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☑ | GET | `P/issue-labels/` | `LabelViewSet.list` (DRF default) class issue/label.py:23; get_queryset issue/label.py:28 | LabelSerializer | ProjectBasePermission | — | web | S |
| ☑ | POST | `P/issue-labels/` | `LabelViewSet.create` issue/label.py:44 | LabelSerializer | AP[A]/PROJ · ProjectBasePermission | invalidate_cache: `/api/workspaces/:slug/labels/` | web | S |
| ☐ | GET | `P/issue-labels/<pk>/` | `LabelViewSet.retrieve` (DRF default) class issue/label.py:23; get_queryset issue/label.py:28 | LabelSerializer | ProjectBasePermission | — | **UNUSED** | S |
| ☐ | PUT | `P/issue-labels/<pk>/` | `LabelViewSet.update` (DRF default) class issue/label.py:23; get_queryset issue/label.py:28 | LabelSerializer | ProjectBasePermission | — | **UNUSED** | S |
| ☑ | PATCH | `P/issue-labels/<pk>/` | `LabelViewSet.partial_update` issue/label.py:59 | LabelSerializer | AP[A]/PROJ · ProjectBasePermission | invalidate_cache: `/api/workspaces/:slug/labels/` | web | S |
| ☑ | DELETE | `P/issue-labels/<pk>/` | `LabelViewSet.destroy` issue/label.py:86 | LabelSerializer | AP[A]/PROJ · ProjectBasePermission | invalidate_cache: `/api/workspaces/:slug/labels/` | web | S |
| ☐ | POST | `P/bulk-create-labels/` | `BulkCreateIssueLabelsEndpoint.post` issue/label.py:92 | LabelSerializer | AP[A]/PROJ | — | **UNUSED** | S |
| ☑ | GET | `P/states/` | `StateViewSet.list` state/base.py:78 | StateSerializer | AP[A,M,G]/PROJ | `?group_by` → dict keyed by state group; boot call | web | S |
| ☑ | POST | `P/states/` | `StateViewSet.create` state/base.py:47 | StateSerializer | AP[A]/PROJ | invalidate_cache: `workspaces/:slug/states/` | web | S |
| ☑ | GET | `P/states/<pk>/` | `StateViewSet.retrieve` (DRF default) class state/base.py:24; get_queryset state/base.py:28 | StateSerializer | IsAuthenticated | — | web | S |
| ☑ | PATCH | `P/states/<pk>/` | `StateViewSet.partial_update` state/base.py:62 | StateSerializer | AP[A,M,G]/PROJ | — | web | S |
| ☑ | DELETE | `P/states/<pk>/` | `StateViewSet.destroy` state/base.py:114 | — (dict/values) | AP[A]/PROJ | invalidate_cache: `workspaces/:slug/states/`. 400 if default state or any issue uses it | web | S |
| ☑ | POST | `P/states/<pk>/mark-default/` | `StateViewSet.mark_as_default` state/base.py:106 | — (dict/values) | AP[A]/PROJ | invalidate_cache: `workspaces/:slug/states/` | web | S |
| ☑ | GET | `W/labels/` | `WorkspaceLabelsEndpoint.get` workspace/label.py:22 | LabelSerializer | WorkspaceViewerPermission | cache_response(2h). Redis response cache (2 h) keyed by user+path | web | S |
| ☑ | GET | `W/states/` | `WorkspaceStatesEndpoint.get` workspace/state.py:21 | StateSerializer | WorkspaceEntityPermission | — | web | S |

### 6. Issues (core)

19 endpoints · 9 L / 7 M / 3 S · 4 unused by web/live

Depends on: issue_filters + ComplexFilterBackend, grouper, order_issue_queryset, Grouped/SubGrouped paginators, `Issue.issue_objects` manager, Issue.save() (advisory lock, sequence_id, sort_order, description_stripped), issue_activity + notifications tasks. `IssueViewSet.list` is the single most important read path: project issues, and (via `?cycle_id`/`?module_id` filters) cycle/module boards in the web.

| ☑ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☑ | GET | `P/issues/list/` | `IssueListEndpoint.get` issue/base.py:85 | IssueSerializer | AP[A,M,G]/PROJ | recent_visited_task | web | L |
| ☑ | GET | `P/issues/` | `IssueViewSet.list` issue/base.py:266 | — (dict/values) | AP[A,M,G]/PROJ | recent_visited_task. core grouped/sub-grouped paginated list (GroupedOffsetPaginator/SubGroupedOffsetPaginator), ComplexFilterBackend `filters=` JSON + legacy issue_filters | web | L |
| ☑ | POST | `P/issues/` | `IssueViewSet.create` issue/base.py:405 | IssueCreateSerializer | AP[A,M]/PROJ | issue_activity(+notif); model_activity†; issue_description_version_task. IssueCreateSerializer writes assignees/labels; Issue.save takes pg_advisory_xact_lock for sequence_id + IssueSequence row; response re-queried with list annotations | web | L |
| ☐ | GET | `P/issues-detail/` | `IssueDetailEndpoint.get` issue/base.py:1028 | IssueListDetailSerializer | AP[A,M,G]/PROJ | paginated detail list incl. issue_relation/sub_issues expand | web | L |
| ☐ | GET | `P/v2/issues/` | `IssuePaginatedViewSet.list` issue/base.py:865 | — (dict/values) | AP[A,M,G]/PROJ | `updated_at__gt` delta sync for local cache (global_paginator) | **UNUSED** (dead web service fn `getIssuesForSync`) | L |
| ☑ | GET | `P/issues/<pk>/` | `IssueViewSet.retrieve` issue/base.py:493 | IssueDetailSerializer | AP[A,M,G]/PROJ+creator(Issue) | recent_visited_task. heavy annotations (cycle_id, module_ids, label_ids, assignee_ids, counts, is_subscribed) + prefetch | web | L  Batch 8: `?expand=issue_attachments` answers each attachment's `asset` as the stored object key; revisit with storage URLs in section 17 |
| ☐ | PUT | `P/issues/<pk>/` | `IssueViewSet.update` (DRF default) class issue/base.py:208; get_queryset issue/base.py:218 | — (dict/values) | IsAuthenticated | — | **UNUSED** | S |
| ☑ | PATCH | `P/issues/<pk>/` | `IssueViewSet.partial_update` issue/base.py:628 | IssueDetailSerializer, IssueCreateSerializer | AP[A,M]/PROJ+creator(Issue) | issue_activity(+notif); model_activity†; issue_description_version_task. activity diff computed in task from requested_data vs current_instance JSON; mentions handled in notification task | web | L |
| ☑ | DELETE | `P/issues/<pk>/` | `IssueViewSet.destroy` issue/base.py:717 | — (dict/values) | AP[A]/PROJ+creator(Issue) | issue_activity(+notif). soft delete → soft_delete_related_objects task | web | M |
| ☑ | DELETE | `P/bulk-delete-issues/` | `BulkDeleteIssuesEndpoint.delete` issue/base.py:775 | — (dict/values) | AP[A]/PROJ | queryset soft delete (UPDATE deleted_at, no cascade task, no activity) + deletes CycleIssue/ModuleIssue | web | M |
| ☑ | POST | `P/bulk-archive-issues/` | `BulkArchiveIssuesEndpoint.post` issue/archive.py:309 | IssueSerializer | AP[A,M]/PROJ · ProjectEntityPermission | issue_activity(+notif). 400 INVALID_ARCHIVE_STATE_GROUP unless completed/cancelled | web | M |
| ☑ | GET | `P/archived-issues/` | `IssueArchiveViewSet.list` issue/archive.py:107 | — (dict/values) | AP[A,M]/PROJ | — | web | L |
| ☑ | GET | `P/issues/<pk>/archive/` | `IssueArchiveViewSet.retrieve` issue/archive.py:221 | IssueDetailSerializer | AP[A,M]/PROJ | — | **UNUSED** (dead web service fn `retrieveArchivedIssue`) | M |
| ☑ | POST | `P/issues/<pk>/archive/` | `IssueArchiveViewSet.archive` issue/archive.py:257 | IssueSerializer | AP[A,M]/PROJ | issue_activity(+notif) | web | M |
| ☑ | DELETE | `P/issues/<pk>/archive/` | `IssueArchiveViewSet.unarchive` issue/archive.py:281 | IssueSerializer | AP[A,M]/PROJ | issue_activity(+notif) | web | M |
| ☐ | GET | `P/deleted-issues/` | `DeletedIssuesListViewSet.get` issue/base.py:802 | — (dict/values) | AP[A,M,G]/PROJ | — | **UNUSED** (dead web service fn `getDeletedIssues`) | S |
| ☑ | POST | `P/issue-dates/` | `IssueBulkUpdateDateEndpoint.post` issue/base.py:1127 | — (dict/values) | AP[A,M]/PROJ | issue_activity. per-issue start/target date validation, bulk_update | web | M |
| ☑ | GET | `P/issues/<issue_id>/meta/` | `IssueMetaEndpoint.get` issue/base.py:1188 | — (dict/values) | AP[A,M,G]/PROJ | `{sequence_id, project_identifier}` | web | S |
| ☑ | GET | `W/work-items/<str:project_identifier>-<str:issue_identifier>/` | `IssueDetailIdentifierEndpoint.get` issue/base.py:1207 | IssueDetailSerializer | IsAuthenticated | recent_visited_task. same payload as IssueViewSet.retrieve, looked up by `PROJ-123` | web | L |

### 7. Issue sub-resources

39 endpoints · 2 L / 22 M / 15 S · 12 unused by web/live

Almost every write here emits `issue_activity(+notif)`; the `history/` endpoint renders those rows, so activity fidelity is visible to users. Web uses v2 attachments (`AP/issues/…/attachments/`); v1 `issue-attachments/` is unused.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `P/issues/<issue_id>/sub-issues/` | `SubIssuesEndpoint.get` issue/sub_issue.py:37 | — (dict/values) | ProjectEntityPermission | annotated sub-issue list + `state_distribution`; optional `group_by` | web | L |
| ☐ | POST | `P/issues/<issue_id>/sub-issues/` | `SubIssuesEndpoint.post` issue/sub_issue.py:210 | IssueSerializer | ProjectEntityPermission | issue_activity(+notif). bulk set parent; activity per child | web | M |
| ☐ | GET | `P/issues/<issue_id>/issue-links/` | `IssueLinkViewSet.list` (DRF default) class issue/link.py:26; get_queryset issue/link.py:32 | IssueLinkSerializer | ProjectEntityPermission | — | web | S |
| ☐ | POST | `P/issues/<issue_id>/issue-links/` | `IssueLinkViewSet.create` issue/link.py:48 | IssueLinkSerializer | ProjectEntityPermission | crawl_work_item_link_title; issue_activity(+notif). `crawl_work_item_link_title` fetches remote page (SSRF-guarded) and writes IssueLink.metadata | web | M |
| ☐ | GET | `P/issues/<issue_id>/issue-links/<pk>/` | `IssueLinkViewSet.retrieve` (DRF default) class issue/link.py:26; get_queryset issue/link.py:32 | IssueLinkSerializer | ProjectEntityPermission | — | **UNUSED** | S |
| ☐ | PUT | `P/issues/<issue_id>/issue-links/<pk>/` | `IssueLinkViewSet.update` (DRF default) class issue/link.py:26; get_queryset issue/link.py:32 | IssueLinkSerializer | ProjectEntityPermission | — | **UNUSED** | S |
| ☐ | PATCH | `P/issues/<issue_id>/issue-links/<pk>/` | `IssueLinkViewSet.partial_update` issue/link.py:71 | IssueLinkSerializer | ProjectEntityPermission | crawl_work_item_link_title; issue_activity(+notif). re-crawl title | web | M |
| ☐ | DELETE | `P/issues/<issue_id>/issue-links/<pk>/` | `IssueLinkViewSet.destroy` issue/link.py:102 | IssueLinkSerializer | ProjectEntityPermission | issue_activity(+notif) | web | M |
| ☐ | GET | `P/issues/<issue_id>/issue-attachments/`<br>`P/issues/<issue_id>/issue-attachments/<pk>/` | `IssueAttachmentEndpoint.get` issue/attachment.py:89 | IssueAttachmentSerializer | AP[A,M,G]/PROJ | — | **UNUSED** | S |
| ☐ | POST | `P/issues/<issue_id>/issue-attachments/`<br>`P/issues/<issue_id>/issue-attachments/<pk>/` | `IssueAttachmentEndpoint.post` issue/attachment.py:38 | IssueAttachmentSerializer | AP[A,M,G]/PROJ | issue_activity(+notif) | **UNUSED** | M |
| ☐ | DELETE | `P/issues/<issue_id>/issue-attachments/`<br>`P/issues/<issue_id>/issue-attachments/<pk>/` | `IssueAttachmentEndpoint.delete` issue/attachment.py:63 | — (dict/values) | AP[A]/PROJ+creator(FileAsset) | issue_activity(+notif) | **UNUSED** | M |
| ☐ | GET | `AP/issues/<issue_id>/attachments/`<br>`AP/issues/<issue_id>/attachments/<pk>/` (unused) | `IssueAttachmentV2Endpoint.get` issue/attachment.py:173 | IssueAttachmentSerializer | AP[A,M,G]/PROJ | with pk → 302 to presigned URL; without → list | web | M |
| ☐ | POST | `AP/issues/<issue_id>/attachments/`<br>`AP/issues/<issue_id>/attachments/<pk>/` (unused) | `IssueAttachmentV2Endpoint.post` issue/attachment.py:100 | IssueAttachmentSerializer | AP[A,M,G]/PROJ | creates FileAsset(is_uploaded=False) + presigned S3 POST | web | M |
| ☐ | PATCH | `AP/issues/<issue_id>/attachments/` (unused)<br>`AP/issues/<issue_id>/attachments/<pk>/` | `IssueAttachmentV2Endpoint.patch` issue/attachment.py:206 | IssueAttachmentSerializer | AP[A,M,G]/PROJ | issue_activity(+notif); get_asset_object_metadata. marks uploaded; `get_asset_object_metadata` (S3 HEAD → FileAsset.storage_metadata) | web | M |
| ☐ | DELETE | `AP/issues/<issue_id>/attachments/` (unused)<br>`AP/issues/<issue_id>/attachments/<pk>/` | `IssueAttachmentV2Endpoint.delete` issue/attachment.py:150 | — (dict/values) | AP[A]/PROJ+creator(FileAsset) | issue_activity(+notif) | web | M |
| ☐ | GET | `P/issues/<issue_id>/history/` | `IssueActivityEndpoint.get` issue/activity.py:30 | IssueActivitySerializer, IssueCommentSerializer | AP[A,M,G]/PROJ · ProjectEntityPermission | merges IssueActivity + IssueComment (with reactions) ordered by created_at; `activity_type` filter | web | M |
| ☐ | GET | `P/issues/<issue_id>/comments/` | `IssueCommentViewSet.list` (DRF default) class issue/comment.py:28; get_queryset issue/comment.py:35 | IssueCommentSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | POST | `P/issues/<issue_id>/comments/` | `IssueCommentViewSet.create` issue/comment.py:64 | IssueCommentSerializer | AP[A,M,G]/PROJ | issue_activity(+notif); model_activity†. comment mentions → notifications | web | M |
| ☐ | GET | `P/issues/<issue_id>/comments/<pk>/` | `IssueCommentViewSet.retrieve` (DRF default) class issue/comment.py:28; get_queryset issue/comment.py:35 | IssueCommentSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | PUT | `P/issues/<issue_id>/comments/<pk>/` | `IssueCommentViewSet.update` (DRF default) class issue/comment.py:28; get_queryset issue/comment.py:35 | IssueCommentSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | PATCH | `P/issues/<issue_id>/comments/<pk>/` | `IssueCommentViewSet.partial_update` issue/comment.py:110 | IssueCommentSerializer | AP[A]/PROJ+creator(IssueComment) | issue_activity(+notif); model_activity† | web | M |
| ☐ | DELETE | `P/issues/<issue_id>/comments/<pk>/` | `IssueCommentViewSet.destroy` issue/comment.py:145 | IssueCommentSerializer | AP[A]/PROJ+creator(IssueComment) | issue_activity(+notif) | web | M |
| ☐ | GET | `P/issues/<issue_id>/issue-subscribers/` | `IssueSubscriberViewSet.list` issue/subscriber.py:52 | ProjectMemberLiteSerializer | ProjectEntityPermission | — | **UNUSED** | S |
| ☐ | POST | `P/issues/<issue_id>/issue-subscribers/` | `IssueSubscriberViewSet.create` (DRF default) class issue/subscriber.py:16; perform_create issue/subscriber.py:30, get_queryset issue/subscriber.py:36 | IssueSubscriberSerializer | ProjectEntityPermission | — | **UNUSED** | S |
| ☐ | DELETE | `P/issues/<issue_id>/issue-subscribers/<subscriber_id>/` | `IssueSubscriberViewSet.destroy` issue/subscriber.py:59 | — (dict/values) | ProjectEntityPermission | — | **UNUSED** | S |
| ☐ | GET | `P/issues/<issue_id>/subscribe/` | `IssueSubscriberViewSet.subscription_status` issue/subscriber.py:97 | — (dict/values) | ProjectEntityPermission | — | web | S |
| ☐ | POST | `P/issues/<issue_id>/subscribe/` | `IssueSubscriberViewSet.subscribe` issue/subscriber.py:69 | IssueSubscriberSerializer | ProjectEntityPermission | — | web | S |
| ☐ | DELETE | `P/issues/<issue_id>/subscribe/` | `IssueSubscriberViewSet.unsubscribe` issue/subscriber.py:87 | — (dict/values) | ProjectEntityPermission | — | web | S |
| ☐ | GET | `P/issues/<issue_id>/reactions/` | `IssueReactionViewSet.list` (DRF default) class issue/reaction.py:25; get_queryset issue/reaction.py:29 | IssueReactionSerializer | IsAuthenticated | — | web | S |
| ☐ | POST | `P/issues/<issue_id>/reactions/` | `IssueReactionViewSet.create` issue/reaction.py:46 | IssueReactionSerializer | AP[A,M,G]/PROJ | issue_activity(+notif) | web | M |
| ☐ | DELETE | `P/issues/<issue_id>/reactions/<str:reaction_code>/` | `IssueReactionViewSet.destroy` issue/reaction.py:65 | — (dict/values) | AP[A,M,G]/PROJ | issue_activity(+notif) | web | M |
| ☐ | GET | `P/comments/<comment_id>/reactions/` | `CommentReactionViewSet.list` (DRF default) class issue/comment.py:163; get_queryset issue/comment.py:167 | CommentReactionSerializer | IsAuthenticated | — | web | S |
| ☐ | POST | `P/comments/<comment_id>/reactions/` | `CommentReactionViewSet.create` issue/comment.py:184 | CommentReactionSerializer | AP[A,M,G]/PROJ | issue_activity(+notif) | web | M |
| ☐ | DELETE | `P/comments/<comment_id>/reactions/<str:reaction_code>/` | `CommentReactionViewSet.destroy` issue/comment.py:213 | — (dict/values) | AP[A,M,G]/PROJ | issue_activity(+notif) | web | M |
| ☐ | GET | `P/issues/<issue_id>/issue-relation/` | `IssueRelationViewSet.list` issue/relation.py:42 | — (dict/values) | ProjectEntityPermission | 6 relation buckets (blocking/blocked_by/duplicate/relates_to/start_*/finish_*) each annotated | web | L |
| ☐ | POST | `P/issues/<issue_id>/issue-relation/` | `IssueRelationViewSet.create` issue/relation.py:209 | RelatedIssueSerializer, IssueRelationSerializer | ProjectEntityPermission | issue_activity(+notif). bulk create, reverse relation mapping (issue_relation_mapper) | web | M |
| ☐ | POST | `P/issues/<issue_id>/remove-relation/` | `IssueRelationViewSet.remove_relation` issue/relation.py:271 | IssueRelationSerializer | ProjectEntityPermission | issue_activity(+notif) | web | M |
| ☐ | GET | `P/issues/<issue_id>/versions/`<br>`P/issues/<issue_id>/versions/<pk>/` | `IssueVersionEndpoint.get` issue/version.py:37 | IssueVersionDetailSerializer | AP[A,M,G]/PROJ | IssueVersion rows are only written by the manual `sync_issue_version`/`issue_task` tasks, never by a view | **UNUSED** | M |
| ☐ | GET | `P/work-items/<work_item_id>/description-versions/`<br>`P/work-items/<work_item_id>/description-versions/<pk>/` | `WorkItemDescriptionVersionEndpoint.get` issue/version.py:87 | IssueDescriptionVersionDetailSerializer | AP[A,M,G]/PROJ | global_paginator cursor | web | M |

### 8. Cycles

22 endpoints · 8 L / 9 M / 5 S · 4 unused by web/live

Cycle status/progress annotations depend on project timezone (`convert_to_utc`, `user_timezone_converter`). Cycle issue lists in web use `CycleIssueViewSet.list` (cycle store) as well as `IssueViewSet.list`.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `P/cycles/` | `CycleViewSet.list` cycle/base.py:184 | — (dict/values) | AP[A,M,G]/PROJ | annotations: status (CASE on dates, project tz), issue counts by state group, progress, assignee/label stats for active cycle | web | L |
| ☐ | POST | `P/cycles/` | `CycleViewSet.create` cycle/base.py:271 | CycleWriteSerializer | AP[A,M]/PROJ | model_activity†. both dates or neither (400); response datetimes converted to project timezone | web | M |
| ☐ | GET | `P/cycles/<pk>/` | `CycleViewSet.retrieve` cycle/base.py:411 | — (dict/values) | AP[A,M]/PROJ | recent_visited_task | web | L |
| ☐ | PUT | `P/cycles/<pk>/` | `CycleViewSet.update` (DRF default) class cycle/base.py:64; get_queryset cycle/base.py:69 | CycleSerializer | IsAuthenticated | — | **UNUSED** | M |
| ☐ | PATCH | `P/cycles/<pk>/` | `CycleViewSet.partial_update` cycle/base.py:336 | CycleSerializer, CycleWriteSerializer | AP[A,M]/PROJ | model_activity†. archived → 400; completed cycle only accepts sort_order; project-tz conversion | web | M |
| ☐ | DELETE | `P/cycles/<pk>/` | `CycleViewSet.destroy` cycle/base.py:478 | — (dict/values) | AP[A]/PROJ+creator(Cycle) | issue_activity(+notif). one issue_activity(cycle.activity.deleted) covering all cycle issues; soft-deletes favorites; hard-deletes recent visits | web | M |
| ☐ | GET | `P/cycles/<cycle_id>/cycle-issues/` | `CycleIssueViewSet.list` cycle/issue.py:110 | — (dict/values) | AP[A,M]/PROJ | — | web | L |
| ☐ | POST | `P/cycles/<cycle_id>/cycle-issues/` | `CycleIssueViewSet.create` cycle/issue.py:224 | — (dict/values) | AP[A,M]/PROJ | issue_activity(+notif). 400 if cycle completed; bulk_create new + bulk_update moved CycleIssue rows; single issue_activity with created/updated lists | web | L |
| ☐ | GET | `P/cycles/<cycle_id>/cycle-issues/<issue_id>/` | `CycleIssueViewSet.retrieve` (DRF default) class cycle/issue.py:40; get_queryset cycle/issue.py:51 | CycleIssueSerializer | IsAuthenticated | — | **UNUSED** | M |
| ☐ | PUT | `P/cycles/<cycle_id>/cycle-issues/<issue_id>/` | `CycleIssueViewSet.update` (DRF default) class cycle/issue.py:40; get_queryset cycle/issue.py:51 | CycleIssueSerializer | IsAuthenticated | — | **UNUSED** | M |
| ☐ | PATCH | `P/cycles/<cycle_id>/cycle-issues/<issue_id>/` | `CycleIssueViewSet.partial_update` (DRF default) class cycle/issue.py:40; get_queryset cycle/issue.py:51 | CycleIssueSerializer | IsAuthenticated | — | **UNUSED** | M |
| ☐ | DELETE | `P/cycles/<cycle_id>/cycle-issues/<issue_id>/` | `CycleIssueViewSet.destroy` cycle/issue.py:320 | — (dict/values) | AP[A,M]/PROJ | issue_activity(+notif) | web | M |
| ☐ | POST | `P/cycles/date-check/` | `CycleDateCheckEndpoint.post` cycle/base.py:522 | — (dict/values) | AP[A,M]/PROJ | — | web | S |
| ☐ | POST | `P/cycles/<cycle_id>/transfer-issues/` | `TransferCycleIssueEndpoint.post` cycle/base.py:596 | — (dict/values) | AP[A,M]/PROJ | issue_activity. `transfer_cycle_issues` (utils/cycle_transfer_issues.py, 478 lines): snapshots progress into Cycle.progress_snapshot, moves incomplete issues, activity per issue | web | L |
| ☐ | GET | `P/cycles/<cycle_id>/user-properties/` | `CycleUserPropertiesEndpoint.get` cycle/base.py:647 | CycleUserPropertiesSerializer | AP[A,M,G]/PROJ | writes on GET | web | S |
| ☐ | PATCH | `P/cycles/<cycle_id>/user-properties/` | `CycleUserPropertiesEndpoint.patch` cycle/base.py:627 | CycleUserPropertiesSerializer | AP[A,M,G]/PROJ | — | web | S |
| ☐ | GET | `P/cycles/<cycle_id>/archive/` (unused)<br>`P/archived-cycles/`<br>`P/archived-cycles/<pk>/` | `CycleArchiveUnarchiveEndpoint.get` cycle/archive.py:272 | — (dict/values) | AP[A,M]/PROJ | huge annotated queryset (49 annotate calls incl. distribution/estimate stats) | web | L |
| ☐ | POST | `P/cycles/<cycle_id>/archive/`<br>`P/archived-cycles/` (unused)<br>`P/archived-cycles/<pk>/` (unused) | `CycleArchiveUnarchiveEndpoint.post` cycle/archive.py:587 | — (dict/values) | AP[A,M]/PROJ | sets archived_at (completed cycles only); removes favorites | web | S |
| ☐ | DELETE | `P/cycles/<cycle_id>/archive/`<br>`P/archived-cycles/` (unused)<br>`P/archived-cycles/<pk>/` (unused) | `CycleArchiveUnarchiveEndpoint.delete` cycle/archive.py:607 | — (dict/values) | AP[A,M]/PROJ | — | web | S |
| ☐ | GET | `P/cycles/<cycle_id>/progress/` | `CycleProgressEndpoint.get` cycle/base.py:660 | — (dict/values) | AP[A,M,G]/PROJ | per-state-group counts + estimate-point sums (points estimates) | web | M |
| ☐ | GET | `P/cycles/<cycle_id>/analytics/` | `CycleAnalyticsEndpoint.get` cycle/base.py:788 | — (dict/values) | AP[A,M,G]/PROJ | assignee/label/completion-chart distributions; `?type=issues\|points` | web | L |
| ☐ | GET | `W/cycles/` | `WorkspaceCyclesEndpoint.get` workspace/cycle.py:22 | CycleSerializer | WorkspaceViewerPermission | — | web | L |

### 9. Modules

25 endpoints · 8 L / 4 M / 13 S · 8 unused by web/live

Module detail/list/archive views share very large annotation blocks (26–56 `annotate` calls) — factor them into one Go query builder. `ModuleIssueViewSet.list` is unused by web.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `P/modules/` | `ModuleViewSet.list` module/base.py:354 | ModuleSerializer | AP[A,M,G]/PROJ | 26 annotations (issue counts per state group, estimate sums, member_ids) | web | L |
| ☐ | POST | `P/modules/` | `ModuleViewSet.create` module/base.py:295 | ModuleWriteSerializer | AP[A,M]/PROJ | model_activity† | web | L |
| ☐ | GET | `P/modules/<pk>/` | `ModuleViewSet.retrieve` module/base.py:396 | ModuleDetailSerializer | AP[A,M]/PROJ | recent_visited_task. assignee/label distribution + completion chart; `estimate_distribution` for points estimates | web | L |
| ☐ | PUT | `P/modules/<pk>/` | `ModuleViewSet.update` (DRF default) class module/base.py:71; get_queryset module/base.py:78 | — (dict/values) | IsAuthenticated | — | **UNUSED** (dead web service fn `updateModule`) | L |
| ☐ | PATCH | `P/modules/<pk>/` | `ModuleViewSet.partial_update` module/base.py:652 | ModuleSerializer, ModuleWriteSerializer | AP[A,M]/PROJ | model_activity† | web | L |
| ☐ | DELETE | `P/modules/<pk>/` | `ModuleViewSet.destroy` module/base.py:724 | — (dict/values) | AP[A]/PROJ+creator(Module) | issue_activity(+notif). issue_activity(module.activity.deleted); deletes ModuleIssue, favorites; hard-deletes recent visits | web | M |
| ☐ | POST | `P/issues/<issue_id>/modules/` | `ModuleIssueViewSet.create_issue_modules` module/issue.py:258 | — (dict/values) | AP[A,M]/PROJ | issue_activity(+notif). adds `modules` + deletes `removed_modules` for one issue; activity per change | web | M |
| ☐ | POST | `P/modules/<module_id>/issues/` | `ModuleIssueViewSet.create_module_issues` module/issue.py:211 | — (dict/values) | AP[A,M]/PROJ | issue_activity(+notif) | web | M |
| ☐ | GET | `P/modules/<module_id>/issues/` | `ModuleIssueViewSet.list` module/issue.py:96 | — (dict/values) | AP[A,M]/PROJ | — | **UNUSED** (dead web service fn `getModuleIssues`) | L |
| ☐ | GET | `P/modules/<module_id>/issues/<issue_id>/` | `ModuleIssueViewSet.retrieve` (DRF default) class module/issue.py:45; get_queryset module/issue.py:84 | ModuleIssueSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | PUT | `P/modules/<module_id>/issues/<issue_id>/` | `ModuleIssueViewSet.update` (DRF default) class module/issue.py:45; get_queryset module/issue.py:84 | ModuleIssueSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | PATCH | `P/modules/<module_id>/issues/<issue_id>/` | `ModuleIssueViewSet.partial_update` (DRF default) class module/issue.py:45; get_queryset module/issue.py:84 | ModuleIssueSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | DELETE | `P/modules/<module_id>/issues/<issue_id>/` | `ModuleIssueViewSet.destroy` module/issue.py:326 | — (dict/values) | AP[A,M]/PROJ | issue_activity(+notif) | web | M |
| ☐ | GET | `P/modules/<module_id>/module-links/` | `ModuleLinkViewSet.list` (DRF default) class module/base.py:762; get_queryset module/base.py:774 | ModuleLinkSerializer | ProjectEntityPermission | — | **UNUSED** | S |
| ☐ | POST | `P/modules/<module_id>/module-links/` | `ModuleLinkViewSet.create` (DRF default) class module/base.py:762; perform_create module/base.py:768, get_queryset module/base.py:774 | ModuleLinkSerializer | ProjectEntityPermission | — | web | S |
| ☐ | GET | `P/modules/<module_id>/module-links/<pk>/` | `ModuleLinkViewSet.retrieve` (DRF default) class module/base.py:762; get_queryset module/base.py:774 | ModuleLinkSerializer | ProjectEntityPermission | — | **UNUSED** | S |
| ☐ | PUT | `P/modules/<module_id>/module-links/<pk>/` | `ModuleLinkViewSet.update` (DRF default) class module/base.py:762; get_queryset module/base.py:774 | ModuleLinkSerializer | ProjectEntityPermission | — | **UNUSED** | S |
| ☐ | PATCH | `P/modules/<module_id>/module-links/<pk>/` | `ModuleLinkViewSet.partial_update` (DRF default) class module/base.py:762; get_queryset module/base.py:774 | ModuleLinkSerializer | ProjectEntityPermission | — | web | S |
| ☐ | DELETE | `P/modules/<module_id>/module-links/<pk>/` | `ModuleLinkViewSet.destroy` (DRF default) class module/base.py:762; get_queryset module/base.py:774 | ModuleLinkSerializer | ProjectEntityPermission | — | web | S |
| ☐ | GET | `P/modules/<module_id>/user-properties/` | `ModuleUserPropertiesEndpoint.get` module/base.py:847 | ModuleUserPropertiesSerializer | AP[A,M,G]/PROJ | writes on GET | web | S |
| ☐ | PATCH | `P/modules/<module_id>/user-properties/` | `ModuleUserPropertiesEndpoint.patch` module/base.py:827 | ModuleUserPropertiesSerializer | AP[A,M,G]/PROJ | — | web | S |
| ☐ | GET | `P/modules/<module_id>/archive/` (unused)<br>`P/archived-modules/`<br>`P/archived-modules/<pk>/` | `ModuleArchiveUnarchiveEndpoint.get` module/archive.py:258 | ModuleDetailSerializer | ProjectEntityPermission | — | web | L |
| ☐ | POST | `P/modules/<module_id>/archive/`<br>`P/archived-modules/` (unused)<br>`P/archived-modules/<pk>/` (unused) | `ModuleArchiveUnarchiveEndpoint.post` module/archive.py:544 | — (dict/values) | ProjectEntityPermission | — | web | S |
| ☐ | DELETE | `P/modules/<module_id>/archive/`<br>`P/archived-modules/` (unused)<br>`P/archived-modules/<pk>/` (unused) | `ModuleArchiveUnarchiveEndpoint.delete` module/archive.py:561 | — (dict/values) | ProjectEntityPermission | — | web | S |
| ☐ | GET | `W/modules/` | `WorkspaceModulesEndpoint.get` workspace/module.py:25 | ModuleSerializer | WorkspaceViewerPermission | boot call (`fetchModulesSlim`) | web | L |

### 10. Estimates

10 endpoints · 0 L / 3 M / 7 S · 4 unused by web/live

Small. `W/estimates/` (boot, cached) and `P/estimates/` are the main reads.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `P/project-estimates/` | `ProjectEstimatePointEndpoint.get` estimate/base.py:36 | EstimatePointSerializer | AP[A,M]/PROJ | — | **UNUSED** | S |
| ☐ | GET | `P/estimates/` | `BulkEstimatePointEndpoint.list` estimate/base.py:54 | EstimateReadSerializer | ProjectEntityPermission | — | web | S |
| ☐ | POST | `P/estimates/` | `BulkEstimatePointEndpoint.create` estimate/base.py:64 | EstimatePointSerializer, EstimateReadSerializer | ProjectEntityPermission | invalidate_cache: `/api/workspaces/:slug/estimates/`. Estimate + bulk_create EstimatePoint | web | M |
| ☐ | GET | `P/estimates/<estimate_id>/` | `BulkEstimatePointEndpoint.retrieve` estimate/base.py:103 | EstimateReadSerializer | ProjectEntityPermission | — | **UNUSED** (dead web service fn `fetchEstimateById`) | S |
| ☐ | PATCH | `P/estimates/<estimate_id>/` | `BulkEstimatePointEndpoint.partial_update` estimate/base.py:109 | EstimateReadSerializer | ProjectEntityPermission | invalidate_cache: `/api/workspaces/:slug/estimates/` | **UNUSED** | M |
| ☐ | DELETE | `P/estimates/<estimate_id>/` | `BulkEstimatePointEndpoint.destroy` estimate/base.py:147 | — (dict/values) | ProjectEntityPermission | invalidate_cache: `/api/workspaces/:slug/estimates/` | web | S |
| ☐ | POST | `P/estimates/<estimate_id>/estimate-points/` | `EstimatePointEndpoint.create` estimate/base.py:155 | EstimatePointSerializer | AP[A,M]/PROJ | — | web | S |
| ☐ | PATCH | `P/estimates/<estimate_id>/estimate-points/<estimate_point_id>/` | `EstimatePointEndpoint.partial_update` estimate/base.py:182 | EstimatePointSerializer | AP[A,M]/PROJ | — | web | S |
| ☐ | DELETE | `P/estimates/<estimate_id>/estimate-points/<estimate_point_id>/` | `EstimatePointEndpoint.destroy` estimate/base.py:197 | EstimatePointSerializer | AP[A,M]/PROJ | issue_activity. `new_estimate_id` re-points issues (issue_activity per issue) before delete | **UNUSED** | M |
| ☐ | GET | `W/estimates/` | `WorkspaceEstimatesEndpoint.get` workspace/estimate.py:22 | WorkspaceEstimateSerializer | WorkspaceEntityPermission | cache_response(2h). Redis response cache 2 h | web | S |

### 11. Saved views

13 endpoints · 1 L / 2 M / 10 S · 2 unused by web/live

`WorkspaceViewIssuesViewSet.list` (`W/issues/`) is the workspace-views issue list — same filter/pagination stack as issues.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `P/views/` | `IssueViewViewSet.list` view/base.py:296 | IssueViewSerializer | AP[A,M,G]/PROJ | annotates is_favorite | web | S |
| ☐ | POST | `P/views/` | `IssueViewViewSet.create` (DRF default) class view/base.py:262; perform_create view/base.py:266, get_queryset view/base.py:269 | IssueViewSerializer | IsAuthenticated | — | web | S |
| ☐ | GET | `P/views/<pk>/` | `IssueViewViewSet.retrieve` view/base.py:315 | IssueViewSerializer | AP[A,M,G]/PROJ | recent_visited_task | web | M |
| ☐ | PUT | `P/views/<pk>/` | `IssueViewViewSet.update` (DRF default) class view/base.py:262; get_queryset view/base.py:269 | IssueViewSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | PATCH | `P/views/<pk>/` | `IssueViewViewSet.partial_update` view/base.py:350 | IssueViewSerializer | AP[—]/PROJ+creator(IssueView) | — | web | S |
| ☐ | DELETE | `P/views/<pk>/` | `IssueViewViewSet.destroy` view/base.py:372 | — (dict/values) | AP[A]/PROJ+creator(IssueView) | owner or admin only; deletes favorites, hard-deletes recent visits | web | M |
| ☐ | GET | `W/views/` | `WorkspaceViewViewSet.list` view/base.py:78 | IssueViewSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | POST | `W/views/` | `WorkspaceViewViewSet.create` (DRF default) class view/base.py:52; perform_create view/base.py:56, get_queryset view/base.py:60 | IssueViewSerializer | IsAuthenticated | — | web | S |
| ☐ | GET | `W/views/<pk>/` | `WorkspaceViewViewSet.retrieve` view/base.py:108 | IssueViewSerializer | IsAuthenticated | recent_visited_task | web | S |
| ☐ | PUT | `W/views/<pk>/` | `WorkspaceViewViewSet.update` (DRF default) class view/base.py:52; get_queryset view/base.py:60 | IssueViewSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | PATCH | `W/views/<pk>/` | `WorkspaceViewViewSet.partial_update` view/base.py:87 | IssueViewSerializer | AP[—]/WS+creator(IssueView) | — | web | S |
| ☐ | DELETE | `W/views/<pk>/` | `WorkspaceViewViewSet.destroy` view/base.py:121 | — (dict/values) | AP[A]/WS+creator(IssueView) | owner or admin only; deletes favorites, hard-deletes recent visits | web | S |
| ☐ | GET | `W/issues/` | `WorkspaceViewIssuesViewSet.list` view/base.py:223 | ViewIssueListSerializer | AP[A,M,G]/WS | workspace-wide issue list across projects where user is an active member (guest restrictions); filters + paginate | web | L |

### 12. Pages

15 endpoints · 1 L / 9 M / 5 S · 1 unused by web/live

apps/live calls `GET/PATCH P/pages/<id>/`, `GET/PATCH P/pages/<id>/description/` and asset GETs with the user's cookie on every collaborative session — port these early if the editor must work. `ProjectPagePermission` (page access/owner rules) gates every row.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `P/pages-summary/` | `PageViewSet.summary` page/base.py:436 | — (dict/values) | ProjectPagePermission | — | **UNUSED** | S |
| ☐ | GET | `P/pages/` | `PageViewSet.list` page/base.py:306 | PageSerializer | ProjectPagePermission | annotations is_favorite, label_ids, project_ids | web | M |
| ☐ | POST | `P/pages/` | `PageViewSet.create` page/base.py:144 | PageSerializer, PageDetailSerializer | ProjectPagePermission | page_transaction. PageSerializer creates Page + ProjectPage; page_transaction parses description_html mentions → PageLog | web | M |
| ☐ | GET | `P/pages/<page_id>/` | `PageViewSet.retrieve` page/base.py:217 | PageDetailSerializer | ProjectPagePermission | recent_visited_task. `?track_visit` (default true) → recent visit; adds `issue_ids` from PageLog; also called by apps/live | live+web | M |
| ☐ | PATCH | `P/pages/<page_id>/` | `PageViewSet.partial_update` page/base.py:169 | PageDetailSerializer | ProjectPagePermission | page_transaction. 400 if locked; parent validation; only owner may change access | web | M |
| ☐ | DELETE | `P/pages/<page_id>/` | `PageViewSet.destroy` page/base.py:383 | — (dict/values) | ProjectPagePermission | must be archived first; owner or ws admin; children parent=NULL; deletes favorites | web | M |
| ☐ | POST | `P/pages/<page_id>/archive/` | `PageViewSet.archive` page/base.py:323 | — (dict/values) | ProjectPagePermission | owner/admin only; recursive CTE sets archived_at on descendants; removes favorites | web | M |
| ☐ | DELETE | `P/pages/<page_id>/archive/` | `PageViewSet.unarchive` page/base.py:354 | — (dict/values) | ProjectPagePermission | recursive CTE clears archived_at | web | M |
| ☐ | POST | `P/pages/<page_id>/lock/` | `PageViewSet.lock` page/base.py:261 | — (dict/values) | ProjectPagePermission | — | web | S |
| ☐ | DELETE | `P/pages/<page_id>/lock/` | `PageViewSet.unlock` page/base.py:273 | — (dict/values) | ProjectPagePermission | — | web | S |
| ☐ | POST | `P/pages/<page_id>/access/` | `PageViewSet.access` page/base.py:286 | — (dict/values) | ProjectPagePermission | — | web | S |
| ☐ | GET | `P/pages/<page_id>/description/` | `PagesDescriptionViewSet.retrieve` page/base.py:516 | — (dict/values) | ProjectPagePermission | streams `description_binary` (Yjs) as application/octet-stream; used by apps/live | live+web | M |
| ☐ | PATCH | `P/pages/<page_id>/description/` | `PagesDescriptionViewSet.partial_update` page/base.py:536 | PageBinaryUpdateSerializer | ProjectPagePermission | page_transaction; track_page_version. called by apps/live on every save: base64 Yjs `description_binary` + html; 400 PAGE_LOCKED/PAGE_ARCHIVED error codes; page_transaction + track_page_version (PageVersion, keeps 20) | live+web | L |
| ☐ | GET | `P/pages/<page_id>/versions/`<br>`P/pages/<page_id>/versions/<pk>/` | `PageVersionEndpoint.get` page/version.py:19 | PageVersionDetailSerializer, PageVersionSerializer | ProjectPagePermission | — | web | S |
| ☐ | POST | `P/pages/<page_id>/duplicate/` | `PageDuplicateEndpoint.post` page/base.py:596 | PageDetailSerializer | ProjectPagePermission | page_transaction; copy_s3_objects_of_description_and_assets. copy page + `copy_s3_objects_of_description_and_assets` (duplicates S3 objects, rewrites asset ids in HTML) | web | M |

### 13. Stickies & home

12 endpoints · 0 L / 2 M / 10 S · 2 unused by web/live

Home page boot: `W/home-preferences/` (lazily creates rows), `W/quick-links/`, `W/recent-visits/` (section 15), `W/stickies/`.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `W/quick-links/` | `QuickLinkViewSet.list` workspace/quick_link.py:61 | WorkspaceUserLinkSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | POST | `W/quick-links/` | `QuickLinkViewSet.create` workspace/quick_link.py:24 | WorkspaceUserLinkSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | GET | `W/quick-links/<pk>/` | `QuickLinkViewSet.retrieve` workspace/quick_link.py:46 | WorkspaceUserLinkSerializer | AP[A,M,G]/WS | — | **UNUSED** | S |
| ☐ | PATCH | `W/quick-links/<pk>/` | `QuickLinkViewSet.partial_update` workspace/quick_link.py:34 | WorkspaceUserLinkSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | DELETE | `W/quick-links/<pk>/` | `QuickLinkViewSet.destroy` workspace/quick_link.py:55 | — (dict/values) | AP[A,M,G]/WS | — | web | S |
| ☐ | GET | `W/home-preferences/`<br>`W/home-preferences/<str:key>/` (unused) | `WorkspaceHomePreferenceViewSet.get` workspace/home.py:24 | — (dict/values) | AP[A,M,G]/WS | writes on GET: bulk_create missing widget keys | web | M |
| ☐ | PATCH | `W/home-preferences/` (unused)<br>`W/home-preferences/<str:key>/` | `WorkspaceHomePreferenceViewSet.patch` workspace/home.py:68 | WorkspaceHomePreferenceSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | GET | `W/stickies/` | `WorkspaceStickyViewSet.list` workspace/sticky.py:41 | StickySerializer | AP[A,M,G]/WS | paginated (per_page/cursor) + `?query` search | web | M |
| ☐ | POST | `W/stickies/` | `WorkspaceStickyViewSet.create` workspace/sticky.py:32 | StickySerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | GET | `W/stickies/<pk>/` | `WorkspaceStickyViewSet.retrieve` (DRF default) class workspace/sticky.py:16; get_queryset workspace/sticky.py:21 | StickySerializer | IsAuthenticated | — | **UNUSED** (dead web service fn `getSticky`) | S |
| ☐ | PATCH | `W/stickies/<pk>/` | `WorkspaceStickyViewSet.partial_update` workspace/sticky.py:55 | StickySerializer | AP[—]/WS+creator(Sticky) | — | web | S |
| ☐ | DELETE | `W/stickies/<pk>/` | `WorkspaceStickyViewSet.destroy` workspace/sticky.py:59 | StickySerializer | AP[—]/WS+creator(Sticky) | — | web | S |

### 14. Notifications

12 endpoints · 1 L / 1 M / 10 S · 1 unused by web/live

`W/users/notifications/unread/` is polled from the top nav. In-app rows are produced by the `notifications` task (see Celery section) — the endpoints are only readers/mutators.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `W/users/notifications/` | `NotificationViewSet.list` notification/base.py:49 | NotificationSerializer | AP[A,M,G]/WS | filters type/snoozed/archived/read/mentioned; `is_mentioned_notification` annotation; ordered snoozed_till,-created_at; paginated | web | L |
| ☐ | GET | `W/users/notifications/<pk>/` | `NotificationViewSet.retrieve` (DRF default) class notification/base.py:33; get_queryset notification/base.py:37 | NotificationSerializer | IsAuthenticated | — | web | S |
| ☐ | PATCH | `W/users/notifications/<pk>/` | `NotificationViewSet.partial_update` notification/base.py:157 | NotificationSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | DELETE | `W/users/notifications/<pk>/` | `NotificationViewSet.destroy` (DRF default) class notification/base.py:33; get_queryset notification/base.py:37 | NotificationSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | POST | `W/users/notifications/<pk>/read/` | `NotificationViewSet.mark_read` notification/base.py:169 | NotificationSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | DELETE | `W/users/notifications/<pk>/read/` | `NotificationViewSet.mark_unread` notification/base.py:177 | NotificationSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | POST | `W/users/notifications/<pk>/archive/` | `NotificationViewSet.archive` notification/base.py:185 | NotificationSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | DELETE | `W/users/notifications/<pk>/archive/` | `NotificationViewSet.unarchive` notification/base.py:193 | NotificationSerializer | AP[A,M,G]/WS | — | web | S |
| ☐ | GET | `W/users/notifications/unread/` | `UnreadNotificationEndpoint.get` notification/base.py:205 | — (dict/values) | AP[A,M,G]/WS | sidebar badge; called frequently | web | S |
| ☐ | POST | `W/users/notifications/mark-all-read/` | `MarkAllReadNotificationViewSet.create` notification/base.py:239 | — (dict/values) | AP[A,M,G]/WS | applies list-style filters then bulk sets read_at | web | M |
| ☐ | GET | `/api/users/me/notification-preferences/` | `UserNotificationPreferenceEndpoint.get` notification/base.py:301 | UserNotificationPreferenceSerializer | IsAuthenticated | — | web | S |
| ☐ | PATCH | `/api/users/me/notification-preferences/` | `UserNotificationPreferenceEndpoint.patch` notification/base.py:307 | UserNotificationPreferenceSerializer | IsAuthenticated | — | web | S |

### 15. Favorites & recents

20 endpoints · 0 L / 3 M / 17 S · 5 unused by web/live

`W/user-favorites/` is a boot call. All entity-specific favorite endpoints write `UserFavorite` (sequence computed in `UserFavorite.save`). Recent visits are written only by `recent_visited_task`: issue/project/page/cycle/module/view retrieves, plus the project issue lists (`P/issues/`, `P/issues/list/`), which record a `project` visit.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `P/user-favorite-cycles/` | `CycleFavoriteViewSet.list` (DRF default) class cycle/base.py:559; get_queryset cycle/base.py:562 | — (dict/values) | IsAuthenticated | — | **UNUSED** | S |
| ☐ | POST | `P/user-favorite-cycles/` | `CycleFavoriteViewSet.create` cycle/base.py:572 | — (dict/values) | AP[A,M]/PROJ | — | web | S |
| ☐ | DELETE | `P/user-favorite-cycles/<cycle_id>/` | `CycleFavoriteViewSet.destroy` cycle/base.py:582 | — (dict/values) | AP[A,M]/PROJ | — | web | S |
| ☐ | GET | `P/user-favorite-modules/` | `ModuleFavoriteViewSet.list` (DRF default) class module/base.py:791; get_queryset module/base.py:795 | — (dict/values) | ProjectLitePermission | — | **UNUSED** | S |
| ☐ | POST | `P/user-favorite-modules/` | `ModuleFavoriteViewSet.create` module/base.py:804 | — (dict/values) | ProjectLitePermission | — | web | S |
| ☐ | DELETE | `P/user-favorite-modules/<module_id>/` | `ModuleFavoriteViewSet.destroy` module/base.py:813 | — (dict/values) | ProjectLitePermission | — | web | S |
| ☐ | POST | `P/favorite-pages/<page_id>/` | `PageFavoriteViewSet.create` page/base.py:491 | — (dict/values) | AP[A,M]/PROJ | — | web | S |
| ☐ | DELETE | `P/favorite-pages/<page_id>/` | `PageFavoriteViewSet.destroy` page/base.py:501 | — (dict/values) | AP[A,M]/PROJ | — | **UNUSED** (dead web service fn `removeFromFavorites`) | S |
| ☐ | GET | `W/user-favorite-projects/` | `ProjectFavoritesViewSet.list` (DRF default) class project/base.py:498; get_queryset project/base.py:501 | — (dict/values) | IsAuthenticated | — | **UNUSED** (dead web service fn `getUserProjectFavorites`) | S |
| ☐ | POST | `W/user-favorite-projects/` | `ProjectFavoritesViewSet.create` project/base.py:514 | — (dict/values) | IsAuthenticated | — | web | S |
| ☐ | DELETE | `W/user-favorite-projects/<project_id>/` | `ProjectFavoritesViewSet.destroy` project/base.py:523 | — (dict/values) | IsAuthenticated | — | web | S |
| ☐ | GET | `P/user-favorite-views/` | `IssueViewFavoriteViewSet.list` (DRF default) class view/base.py:407; get_queryset view/base.py:410 | — (dict/values) | IsAuthenticated | — | **UNUSED** | S |
| ☐ | POST | `P/user-favorite-views/` | `IssueViewFavoriteViewSet.create` view/base.py:420 | — (dict/values) | AP[A,M]/PROJ | — | web | S |
| ☐ | DELETE | `P/user-favorite-views/<view_id>/` | `IssueViewFavoriteViewSet.destroy` view/base.py:430 | — (dict/values) | AP[A,M]/PROJ | — | web | S |
| ☐ | GET | `W/user-favorites/`<br>`W/user-favorites/<favorite_id>/` (unused) | `WorkspaceFavoriteEndpoint.get` workspace/favorite.py:24 | UserFavoriteSerializer | AP[A,M]/WS | boot call; root favorites (parent NULL) the user can still see; UserFavoriteSerializer builds `entity_data` per entity_type | web | M |
| ☐ | POST | `W/user-favorites/`<br>`W/user-favorites/<favorite_id>/` (unused) | `WorkspaceFavoriteEndpoint.post` workspace/favorite.py:38 | UserFavoriteSerializer | AP[A,M]/WS | folder/entity favorites; sequence calc in UserFavorite.save | web | M |
| ☐ | PATCH | `W/user-favorites/` (unused)<br>`W/user-favorites/<favorite_id>/` | `WorkspaceFavoriteEndpoint.patch` workspace/favorite.py:70 | UserFavoriteSerializer | AP[A,M]/WS | — | web | S |
| ☐ | DELETE | `W/user-favorites/` (unused)<br>`W/user-favorites/<favorite_id>/` | `WorkspaceFavoriteEndpoint.delete` workspace/favorite.py:79 | — (dict/values) | AP[A,M]/WS | — | web | S |
| ☐ | GET | `W/user-favorites/<favorite_id>/group/` | `WorkspaceFavoriteGroupEndpoint.get` workspace/favorite.py:87 | UserFavoriteSerializer | AP[A,M]/WS | — | web | S |
| ☐ | GET | `W/recent-visits/` | `UserRecentVisitViewSet.list` workspace/recent_visit.py:25 | WorkspaceRecentVisitSerializer | AP[A,M,G]/WS | `?entity_name`; only issue/page/project; top 20; serializer resolves `entity_data` per type | web | M |

### 16. Search

3 endpoints · 1 L / 2 M / 0 S · 0 unused by web/live

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `W/search/` | `GlobalSearchEndpoint.get` search/base.py:271 | — (dict/values) | IsAuthenticated | icontains over workspace/project/issue/cycle/module/issue_view/page/intake (intake CUT → return []); `?entities=` subset | web | M |
| ☐ | GET | `P/search-issues/` | `IssueSearchEndpoint.get` search/issue.py:99 | — (dict/values) | IsAuthenticated | `search_issues` util + filters (parent/issue_relation/cycle/module/sub_issue exclusion) | web | M |
| ☐ | GET | `W/entity-search/` | `SearchEndpoint.get` search/base.py:308 | — (dict/values) | WorkspaceUserPermission | mention/link picker: `query_type`=user_mention,project,issue,cycle,module,page; project- or workspace-scoped; 423 lines | web | L |

### 17. File assets

25 endpoints · 0 L / 12 M / 13 S · 5 unused by web/live

All v2 endpoints implement the presigned-upload protocol: POST → FileAsset(is_uploaded=False) + presigned POST form; client uploads to S3; PATCH marks uploaded and enqueues `get_asset_object_metadata`; GET 302-redirects to a presigned URL. `StaticFileAssetEndpoint` is reached via `avatar_url`/`logo_url`/`cover_image_url` values emitted by serializers.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `W/file-assets/`<br>`/api/workspaces/file-assets/<workspace_id>/<str:asset_key>/` | `FileAssetEndpoint.get` asset/base.py:26 | FileAssetSerializer | WorkspaceMemberPermission | — | **UNUSED** | S |
| ☐ | POST | `W/file-assets/`<br>`/api/workspaces/file-assets/<workspace_id>/<str:asset_key>/` | `FileAssetEndpoint.post` asset/base.py:38 | FileAssetSerializer | WorkspaceMemberPermission | — | **UNUSED** | S |
| ☐ | DELETE | `W/file-assets/` (unused)<br>`/api/workspaces/file-assets/<workspace_id>/<str:asset_key>/` | `FileAssetEndpoint.delete` asset/base.py:48 | — (dict/values) | WorkspaceMemberPermission | — | web | S |
| ☐ | GET | `/api/users/file-assets/`<br>`/api/users/file-assets/<str:asset_key>/` | `UserAssetsEndpoint.get` asset/base.py:70 | FileAssetSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | POST | `/api/users/file-assets/`<br>`/api/users/file-assets/<str:asset_key>/` | `UserAssetsEndpoint.post` asset/base.py:81 | FileAssetSerializer | IsAuthenticated | — | **UNUSED** | S |
| ☐ | DELETE | `/api/users/file-assets/` (unused)<br>`/api/users/file-assets/<str:asset_key>/` | `UserAssetsEndpoint.delete` asset/base.py:88 | — (dict/values) | IsAuthenticated | — | web | S |
| ☐ | POST | `/api/workspaces/file-assets/<workspace_id>/<str:asset_key>/restore/` | `FileAssetViewSet.restore` asset/base.py:59 | — (dict/values) | WorkspaceMemberPermission | — | web | S |
| ☐ | GET | `AW/` (unused)<br>`AW/<asset_id>/` | `WorkspaceFileAssetEndpoint.get` asset/v2.py:462 | — (dict/values) | AP[A,M,G]/WS | 302 to presigned GET URL | editor+live | M |
| ☐ | POST | `AW/`<br>`AW/<asset_id>/` (unused) | `WorkspaceFileAssetEndpoint.post` asset/v2.py:341 | — (dict/values) | AP[A,M,G]/WS | FileAsset(is_uploaded=False) + presigned POST; entity_type switch (logo, cover, page/sticky description…) | web | M |
| ☐ | PATCH | `AW/` (unused)<br>`AW/<asset_id>/` | `WorkspaceFileAssetEndpoint.patch` asset/v2.py:418 | — (dict/values) | AP[A,M,G]/WS | get_asset_object_metadata. marks uploaded + attributes; `get_asset_object_metadata` | web | M |
| ☐ | DELETE | `AW/` (unused)<br>`AW/<asset_id>/` | `WorkspaceFileAssetEndpoint.delete` asset/v2.py:446 | — (dict/values) | AP[A,M,G]/WS | soft delete + unlink entity (e.g. workspace logo) | web | S |
| ☐ | POST | `/api/assets/v2/user-assets/`<br>`/api/assets/v2/user-assets/<asset_id>/` (unused) | `UserAssetsV2Endpoint.post` asset/v2.py:111 | — (dict/values) | IsAuthenticated | avatar/cover upload; presigned POST | web | M |
| ☐ | PATCH | `/api/assets/v2/user-assets/` (unused)<br>`/api/assets/v2/user-assets/<asset_id>/` | `UserAssetsV2Endpoint.patch` asset/v2.py:172 | — (dict/values) | IsAuthenticated | get_asset_object_metadata. sets User.avatar_asset/cover_image_asset | web | M |
| ☐ | DELETE | `/api/assets/v2/user-assets/` (unused)<br>`/api/assets/v2/user-assets/<asset_id>/` | `UserAssetsV2Endpoint.delete` asset/v2.py:193 | — (dict/values) | IsAuthenticated | — | web | S |
| ☐ | POST | `AW/restore/<asset_id>/` | `AssetRestoreEndpoint.post` asset/v2.py:540 | — (dict/values) | AP[A,M,G]/WS | — | web | S |
| ☐ | GET | `/api/assets/v2/static/<asset_id>/` | `StaticFileAssetEndpoint.get` asset/v2.py:496 | — (dict/values) | AllowAny | AllowAny; 302 to presigned URL; only USER_AVATAR/USER_COVER/WORKSPACE_LOGO/PROJECT_COVER | web (indirect: `FileAsset.asset_url` → avatar_url/logo_url/cover_image_url in API payloads) | S |
| ☐ | GET | `AP/` (unused)<br>`AP/<pk>/` | `ProjectAssetEndpoint.get` asset/v2.py:675 | — (dict/values) | AP[A,M,G]/PROJ | 302 to presigned URL | editor+live | M |
| ☐ | POST | `AP/`<br>`AP/<pk>/` (unused) | `ProjectAssetEndpoint.post` asset/v2.py:581 | — (dict/values) | AP[A,M,G]/PROJ | FileAsset(is_uploaded=False) + presigned POST | web | M |
| ☐ | PATCH | `AP/` (unused)<br>`AP/<pk>/` | `ProjectAssetEndpoint.patch` asset/v2.py:648 | — (dict/values) | AP[A,M,G]/PROJ | get_asset_object_metadata | web | M |
| ☐ | DELETE | `AP/`<br>`AP/<pk>/` | `ProjectAssetEndpoint.delete` asset/v2.py:664 | — (dict/values) | AP[A,M,G]/PROJ | — | **UNUSED** | S |
| ☐ | POST | `AP/<entity_id>/bulk/` | `ProjectBulkAssetEndpoint.post` asset/v2.py:705 | — (dict/values) | AP[A,M,G]/PROJ | attach uploaded asset ids to entity (issue/page/comment description) | web | M |
| ☐ | GET | `AW/check/<asset_id>/` | `AssetCheckEndpoint.get` asset/v2.py:774 | — (dict/values) | AP[A,M,G]/WS | `{exists: bool}` | web | S |
| ☐ | POST | `AW/duplicate-assets/<asset_id>/` | `DuplicateAssetEndpoint.post` asset/v2.py:816 | — (dict/values) | AP[A,M,G]/WS | S3 copy_object + new FileAsset | web | M |
| ☐ | GET | `AW/download/<asset_id>/` | `WorkspaceAssetDownloadEndpoint.get` asset/v2.py:872 | — (dict/values) | AP[A,M,G]/WS | 302 to presigned URL with attachment disposition | editor | M |
| ☐ | GET | `AP/download/<asset_id>/` | `ProjectAssetDownloadEndpoint.get` asset/v2.py:899 | — (dict/values) | AP[A,M,G]/PROJ | 302 to presigned URL with attachment disposition | editor | M |

### 18. Misc (timezones, user activity & profile pages)

10 endpoints · 4 L / 5 M / 1 S · 4 unused by web/live

Profile pages (`W/user-*/<user_id>/`) reuse issue filters/grouper; `users/me/workspaces/<slug>/*-graph/` and `…/dashboard/` are dead.

| ☐ | Method | URL | View · file:line | Serializer(s) | Permission | Side effects | Caller | Cx |
|---|---|---|---|---|---|---|---|---|
| ☐ | GET | `/api/timezones/` | `TimezoneEndpoint.get` timezone/base.py:29 | — (dict/values) | AllowAny | cache_page(2h). static list; page cached 2 h | web | S |
| ☐ | GET | `/api/users/me/activities/` | `UserActivityEndpoint.get` user/base.py:382 | IssueActivitySerializer | IsAuthenticated | — | **UNUSED** | M |
| ☐ | GET | `/api/users/me/workspaces/<str:slug>/activity-graph/` | `UserActivityGraphEndpoint.get` workspace/user.py:533 | — (dict/values) | IsAuthenticated | — | **UNUSED** | M |
| ☐ | GET | `/api/users/me/workspaces/<str:slug>/issues-completed-graph/` | `UserIssueCompletedGraphEndpoint.get` workspace/user.py:550 | — (dict/values) | IsAuthenticated | — | **UNUSED** | M |
| ☐ | GET | `/api/users/me/workspaces/<str:slug>/dashboard/` | `UserWorkspaceDashboardEndpoint.get` workspace/base.py:234 | — (dict/values) | IsAuthenticated | legacy dashboard aggregates | **UNUSED** | L |
| ☐ | GET | `W/user-stats/<user_id>/` | `WorkspaceUserProfileStatsEndpoint.get` workspace/user.py:406 | — (dict/values) | IsAuthenticated | state/priority distribution, created/assigned/completed/subscribed counts | web | L |
| ☐ | GET | `W/user-activity/<user_id>/` | `WorkspaceUserActivityEndpoint.get` workspace/user.py:378 | IssueActivitySerializer | WorkspaceEntityPermission | paginated IssueActivity | web | M |
| ☐ | POST | `W/user-activity/<user_id>/export/` | `ExportWorkspaceUserActivityEndpoint.post` workspace/base.py:350 | — (dict/values) | WorkspaceEntityPermission | synchronous CSV download (no task) | web | M |
| ☐ | GET | `W/user-profile/<user_id>/` | `WorkspaceUserProfileEndpoint.get` workspace/user.py:281 | — (dict/values) | IsAuthenticated | per-project counts for user | web | L |
| ☐ | GET | `W/user-issues/<user_id>/` | `WorkspaceUserProfileIssuesEndpoint.get` workspace/user.py:135 | — (dict/values) | WorkspaceViewerPermission | grouped/paginated issue list like IssueViewSet.list | web | L |

## 1. Shared building blocks (port once)

Paths are relative to `apps/api/plane/`.

### Request pipeline

| Piece | Where | What it does |
|---|---|---|
| `BaseViewSet` / `BaseAPIView` | `app/views/base.py:48`, `:149` | Base for almost every view. Adds `TimezoneMixin` (`:34`, activates `user.user_timezone`, so every serialized datetime is rendered in the user's tz) and the exception mapping in `handle_exception`: `IntegrityError`→400 `{"error":"The payload is not valid"}`, `ValidationError`→400 `"Please provide valid detail"`, `ObjectDoesNotExist`→404 `"The required object does not exist."`, `KeyError`→400 `"The required key does not exist."`, otherwise 500 `"Something went wrong please try again later"`. Also provides `?fields=` / `?expand=` parsing and `filter_queryset` (DjangoFilterBackend + SearchFilter: `search_fields` → `?search=`, `filterset_fields`). |
| `BaseSessionAuthentication` | `authentication/session.py:8` | Session auth with **CSRF disabled** for all `Base*` views. Plain `APIView`s (`/auth/change-password/`, `/auth/set-password/`, …) use DRF's `SessionAuthentication`, which **does** enforce CSRF for logged-in users. |
| DRF settings | `settings/common.py:139` | Default permission `IsAuthenticated` (401 via `auth_exception_handler` in `authentication/adapter/exception.py`; throttling → 429 `RATE_LIMIT_EXCEEDED`), `AnonRateThrottle` 30/min, `asset_id` 5/min (`AssetRateThrottle` on `DuplicateAssetEndpoint`), `EmailVerificationThrottle` 3/h on email-code generation, `AuthenticationThrottle` on `/api/timezones/`, JSON renderer. |
| `DynamicBaseSerializer` | `app/serializers/base.py:91` (expansion map `:30`) | `fields=` / `expand=` support used by issue, project, cycle and module serializers. `expand` swaps `<rel>_id` for a nested object (or `null`). |
| `S3Storage` | `settings/storage.py:19` | Presigned POST/GET generation and `head_object`, used by all asset endpoints and the attachment v2 endpoints. |
| `base_host` | `utils/host.py:17` | Builds the absolute origin passed into tasks and emails (`origin=`). |

### Permissions (`app/permissions/`)

| Piece | Where | Rule |
|---|---|---|
| `allow_permission(roles, level, creator, model)` + `ROLE` (A=20, M=15, G=5) | `base.py:19`, `:13` | `WORKSPACE`: an active `WorkspaceMember` with a role in the list. `PROJECT` (default): an active `ProjectMember` with a role in the list, **or** any active `ProjectMember` who is also a workspace admin. `creator=True`: passes if `model.objects.filter(id=kwargs["pk"], created_by=user)` exists, after an active-workspace-member check. Denial is 403 `{"error":"You don't have the required permissions."}`, a different body from DRF permission classes (`{"detail": …}`). |
| `WorkSpaceBasePermission` | `workspace.py:19` | POST and safe methods open to any user. PUT/PATCH need workspace A/M. DELETE needs A. |
| `WorkSpaceAdminPermission` / `WorkspaceOwnerPermission` | `workspace.py:61`, `:51` | Workspace A/M, or A only. |
| `WorkspaceEntityPermission` | `workspace.py:74` | Safe methods: any active member. Writes: A/M. |
| `WorkspaceViewerPermission` / `WorkspaceUserPermission` | `workspace.py:93`, `:103` | Any active workspace member. |
| `WorkspaceMemberPermission` | `workspace.py:113` | Active member, workspace resolved from `slug` **or** `workspace_id` (legacy file-asset routes). |
| `ProjectBasePermission` | `project.py:13` | Safe: workspace member. POST: workspace A/M. Otherwise project admin, or workspace admin who is a project member. |
| `ProjectMemberPermission` / `ProjectEntityPermission` / `ProjectLitePermission` / `ProjectAdminPermission` | `project.py:56`, `:88`, `:136`, `:122` | Variants of the project-membership checks (Entity: safe = any project member, write = A/M. Lite: any project member). |
| `ProjectPagePermission` | `page.py:18` | Requires project membership. The page must be linked to the URL project through an active `ProjectPage`. The owner always passes. Private pages are owner-only. Public pages: POST/PUT/PATCH need A/M, GET is open to all roles, DELETE needs A. |

### Querysets, filters, grouping, pagination

| Piece | Where | What it does |
|---|---|---|
| Model managers | `db/mixins.py:48-84`, `db/models/issue.py:92`, `db/models/state.py:65-97` | `SoftDeletionManager` hides `deleted_at IS NOT NULL` (`all_objects` sees everything). `Issue.issue_objects` also excludes triage-state, archived, archived-project and draft issues. `State.objects` excludes triage states and `State.triage_objects` returns only them. **Most list endpoints depend on these implicit filters.** |
| Soft delete | `db/mixins.py:61` (`SoftDeleteModel.delete`) | `instance.delete()` sets `deleted_at` and enqueues `soft_delete_related_objects`, which cascades `deleted_at` to FK children. `queryset.delete()` only runs an UPDATE, with **no cascade**. `Workspace.delete` also rewrites the slug to `slug__<epoch>` (`db/models/workspace.py:156`). |
| Model `save()` hooks | `db/models/issue.py:180`, `state.py:117`, `label.py:46`, `module.py:115`, `cycle.py:88`, `project.py:167/187/226`, `page.py:70`, `sticky.py:38`, `favorite.py:52`, `view.py:79`, `user.py:169/298` | Issue: `pg_advisory_xact_lock(project)`, then `sequence_id = max+1`, plus an `IssueSequence` row, `sort_order` = max in state + 10000, `description_stripped`, default state, `completed_at` sync. State: slug + sequence +15000. Label: sort_order +10000. Project: uppercases identifier and inherits workspace timezone. `ProjectBaseModel`: copies `workspace_id` from project. `ProjectMember.save`: creates `ProjectUserProperty`. `UserFavorite` / `Sticky` / `Module` / `Cycle` / `IssueView`: ordering fields. A `User` `post_save` signal creates `UserNotificationPreference`. Go needs these in its repository layer. |
| Common issue annotations | `app/views/issue/base.py:218` (`IssueViewSet.get_queryset`), copied into `cycle/issue.py`, `module/issue.py`, `issue/archive.py`, `issue/sub_issue.py`, `issue/relation.py`, `view/base.py`, `workspace/user.py` | `cycle_id` subquery, `link_count`, `attachment_count`, `sub_issues_count`. Port once as a SQL fragment. |
| `issue_queryset_grouper` / `issue_on_results` / `issue_group_values` | `utils/grouper.py:28`, `:93`, `:144` | `ArrayAgg` subqueries for `assignee_ids`, `label_ids` and `module_ids` (skipping soft-deleted links and archived modules), the result shaping used by every issue list, and the list of group keys for a `group_by` field. |
| `order_issue_queryset` / `sanitize_order_by` / `ISSUE_GROUP_BY_ALLOWLIST` | `utils/order_queryset.py:153`, `:129`, `:93` | Priority and state-group CASE ordering, `labels__name` / `assignees__first_name` / `issue_module__module__name` ordering via Max/Min aggregates, and allow-lists that stop `order_by`/`group_by` injection. |
| `issue_filters` | `utils/issue_filters.py:428` | Legacy query-param filters (`state`, `priority`, `labels`, `assignees`, `mentions`, dates with `2_weeks;after;fromnow` syntax, `cycle`, `module`, `sub_issue`, `subscriber`, …) → Django `Q` kwargs. |
| `ComplexFilterBackend` + `IssueFilterSet` + `LegacyToRichFiltersConverter` | `utils/filters/filter_backend.py:20`, `filterset.py:135`, `converters.py:13` | The JSON `?filters=` rich-filter language (and/or/not trees, max depth 5) used by issue lists in issue/base, archive, cycle/issue, module/issue, view/base and workspace/user views. |
| `BasePaginator.paginate` + `Cursor` + `OffsetPaginator` / `GroupedOffsetPaginator` / `SubGroupedOffsetPaginator` | `utils/paginator.py:636/655`, `:22`, `:94`, `:195`, `:390` | Cursor format `per_page:page:is_prev` (default `1000:0:0`, `per_page` ≤ max). Response envelope: `grouped_by`, `sub_grouped_by`, `total_count`, `next_cursor`, `prev_cursor`, `next_page_results`, `prev_page_results`, `count`, `total_pages`, `total_results`, `extra_stats`, `results`. Grouped variants page *within each group* (ROW_NUMBER window per group), return `{"<group>": {"results": [...], "total_results": n}}` (sub-grouped adds a nested level) with every `group_by_fields` key present even when empty, and validate `group_by` against the allow-list. **Byte-exact parity is the hardest contract in the port.** |
| `paginate` (global) + `PaginateCursor` | `utils/global_paginator.py:33`, `:12` | Simpler cursor paginator used by `v2/issues/`, issue versions and description versions. |
| `user_timezone_converter` / `convert_to_utc` / `convert_utc_to_project_timezone` | `utils/timezone_converter.py:17`, `:42`, `:97` | Post-processes `.values()` rows so datetimes render in the user's tz. Converts cycle dates between the project tz and UTC. |
| `search_issues` | `utils/issue_search.py:14` | OR of `name`/`project__identifier` icontains and an exact `sequence_id` match for every number in the query (if ≤20 chars). Used by `search-issues/`. |
| `get_inverse_relation` / `get_actual_relation` | `utils/issue_relation_mapper.py:5`, `:19` | Maps `blocking`↔`blocked_by`, `start_after`↔`start_before`, `finish_after`↔`finish_before`. |
| `transfer_cycle_issues` | `utils/cycle_transfer_issues.py:35` | Cycle-completion transfer: snapshots progress into `Cycle.progress_snapshot` and moves incomplete issues (478 lines). |
| `validate_html_content` / `validate_binary_data` | `utils/content_validator.py:211`, `:29` | Sanitization and validation used by issue, comment and page serializers on description writes. |

Ported in batch 8, for the cycle, module, view and workspace lists to reuse:
`internal/api/issue_query.go` (`issueQuery`, which follows Django's join-reuse rules
so multi-valued joins multiply and collapse rows as Django's do; `ComplexFilterBackend` with
`IssueFilterSet`; legacy `issue_filters`), `internal/api/issue_list.go` (`order_issue_queryset`,
`issue_queryset_grouper`, `issue_group_values`, and the offset, grouped and sub-grouped
paginators) and `internal/sanitize` (`validate_html_content`'s nh3, plus mention extraction).
The tasks are in `internal/api/issue_activity.go`.

### Caching, tasks, side-effect helpers

| Piece | Where | What it does |
|---|---|---|
| `cache_response(timeout, path, user)` | `utils/cache.py:25` | Caches 200 responses in Redis under `path[:user_id]` (skipped when DEBUG). Used by `W/labels/` and `W/estimates/` (2 h). Django's `cache_page(2h)` caches `/api/timezones/`, and `cache_control(private, max_age=12)` + `vary_on_cookie` covers `users/me/`, `settings/` and `profile/`. |
| `invalidate_cache` (decorator) / `invalidate_cache_directly` | `utils/cache.py:72`, `:54` | Deletes the cached key(s) above after a write (`:slug` templating). If Go never caches, these are no-ops. Otherwise port both. |
| `issue_activity` task | `bgtasks/issue_activities_task.py:1504` | Takes `type` (e.g. `issue.activity.updated`, `comment.activity.created`, `cycle.activity.created`, …, 27 mapped types), `requested_data` / `current_instance` as JSON strings, and an `epoch`. Computes per-field diffs (`track_name`, `track_state`, `track_labels`, `track_assignees`, …) → `bulk_create IssueActivity`. Also auto-subscribes assignees (`IssueSubscriber`), bumps `issues.updated_at`, stores `origin` in Redis for 10 min, and chains `notifications` when `notification=True`. 1,600 lines, and the `history/` endpoint renders its output verbatim. |
| `notifications` task | `bgtasks/notification_task.py:191` | Resolves subscribers, assignees, creators and mentions (`IssueMention` add/remove, subscriber `get_or_create`) and checks `UserNotificationPreference`. Writes `Notification` + `EmailNotificationLog` rows (`bulk_create`). |
| `recent_visited_task` | `bgtasks/recent_visited_task.py:18` | Upserts `UserRecentVisit(entity_name, entity_identifier, user, project, workspace)`, capped at 20 rows per user per workspace (oldest deleted). |
| `model_activity` / `webhook_activity` | `bgtasks/webhook_task.py:479`, `:393` | Webhook fan-out only. **Drop.** |
| `page_transaction` / `track_page_version` | `bgtasks/page_transaction_task.py:85`, `bgtasks/page_version_task.py:22` | Parse mention/embed components out of page HTML → `PageLog` add/remove. Snapshot `PageVersion` when `description_html` changes (update the latest if same user within `PAGE_VERSION_TASK_TIMEOUT`, keep at most 20). |
| `issue_description_version_task` | `bgtasks/issue_description_version_task.py:44` | Creates `IssueDescriptionVersion`, or updates the latest one if it belongs to the same user and is ≤600 s old. |

## 2. Celery tasks the kept features depend on

### Event-driven

| Task | File:line | Triggered by | Writes |
|---|---|---|---|
| `issue_activity` | `bgtasks/issue_activities_task.py:1504` | Nearly every issue / comment / link / attachment / reaction / relation / cycle-issue / module-issue / estimate-point write | `issue_activities`, `issue_subscribers`, `issues.updated_at`. Redis `str(issue_id)` → origin (10 min). Chains `notifications` |
| `notifications` | `bgtasks/notification_task.py:191` | `issue_activity` with `notification=True` | `notifications`, `email_notification_logs`, `issue_subscribers`, `issue_mentions` |
| `recent_visited_task` | `bgtasks/recent_visited_task.py:18` | Issue / project / page / cycle / module / view retrieve, project issue lists | `user_recent_visits` |
| `issue_description_version_task` | `bgtasks/issue_description_version_task.py:44` | Issue create and update (description change) | `issue_description_versions` |
| `page_transaction` | `bgtasks/page_transaction_task.py:85` | Page create / update / description PATCH / duplicate | `page_logs` |
| `track_page_version` | `bgtasks/page_version_task.py:22` | Page description PATCH | `page_versions` (≤20 per page) |
| `crawl_work_item_link_title` | `bgtasks/work_item_link_task.py:249` | Issue link create / update | `issue_links.metadata` (outbound HTTP with SSRF guard `validate_url_ip`, favicon base64) |
| `get_asset_object_metadata` | `bgtasks/storage_metadata_task.py:15` | Asset / attachment PATCH (upload confirmed) | `file_assets.storage_metadata` (S3 HEAD) |
| `copy_s3_objects_of_description_and_assets` | `bgtasks/copy_s3_object.py:125` | Page duplicate | New `file_assets` rows, S3 `copy_object`, rewritten entity `description_html` / `description_binary`. **Calls apps/live `POST {LIVE_URL}/convert-document/`** |
| `soft_delete_related_objects` | `bgtasks/deletion_task.py:18` | Any `instance.delete()` on a `SoftDeleteModel` (issue, project, cycle, module, page, view, label, state, workspace, comment, link, …) | `deleted_at` on FK-related rows |
| `workspace_seed` | `bgtasks/workspace_seed_task.py:505` | Workspace create | Bot `users` + `workspace_members`, demo `projects`, `project_members`, `project_user_properties`, `states`, `labels`, `issues`, `issue_sequences`, `issue_activities`, `issue_labels`, `cycles`, `cycle_issues`, `modules`, `module_issues`, `pages`, `project_pages`, `issue_views` (optional for plane-lite) |
| Email tasks: `magic_link`, `forgot_password`, `workspace_invitation`, `project_invitation`, `project_add_user_email`, `send_email_update_magic_code`, `send_email_update_confirmation`, `user_deactivation_email`, `user_activation_email` | `bgtasks/magic_link_code_task.py:23`, `forgot_password_task.py:23`, `workspace_invitation_task.py:23`, `project_invitation_task.py:24`, `project_add_user_email_task.py:25`, `user_email_update_task.py:22/67`, `user_deactivation_email_task.py:23`, `user_activation_email_task.py:23` | Auth, invite, member-add, email-change and deactivate flows (`user_activation_email` is sent from `authentication/adapter/base.py:251` on reactivating login) | SMTP only. The two invitation tasks also `save()` their invite row |
| `model_activity` → `webhook_activity` → `webhook_send_task` | `bgtasks/webhook_task.py:479/393/242` | Project / issue / cycle / module / comment writes | `webhook_logs` (CUT). **Drop** |

### Periodic (`plane/celery.py:44` beat schedule; `django_celery_beat.DatabaseScheduler`)

| Job | Task | Schedule (UTC) | Still relevant? |
|---|---|---|---|
| Email notification digest | `email_notification_task.stack_email_notification` (`:47`) → `send_email_notification` (`:153`) | every 5 min | Only if notification **emails** are kept. In-app only = drop. Uses a Redis lock and updates `email_notification_logs.processed_at` / `sent_at` |
| Hard delete | `deletion_task.hard_delete` (`:114`) | 00:00 | **Yes**: purges rows soft-deleted more than `HARD_DELETE_AFTER_DAYS` ago (workspace, project, cycle, module, issue, page, view, label, state, activity, comment, link, reaction, favorite, cycle/module issue, estimate, estimate point, …) |
| Auto archive / close | `issue_automation_task.archive_and_close_old_issues` (`:23`) | 01:00 | **Yes** if project `archive_in` / `close_in` settings are kept. Bulk-updates `issues.archived_at` / `state_id` and emits `issue_activity` |
| Unuploaded asset cleanup | `file_asset_task.delete_unuploaded_file_asset` (`:21`) | 02:00 | **Yes**: soft-deletes `file_assets` with `is_uploaded=False` older than `UNUPLOADED_ASSET_DELETE_DAYS` (default 7) |
| Email log cleanup | `cleanup_task.delete_email_notification_logs` (`:172`) | 02:45 | Yes if `email_notification_logs` keep being written |
| Page version cleanup | `cleanup_task.delete_page_versions` (`:182`) | 03:00 | Yes |
| Issue description version cleanup | `cleanup_task.delete_issue_description_versions` (`:192`) | 03:15 | Yes |
| Instance telemetry | `license.bgtasks.telemetry_metrics.push_instance_metrics` | every N min | No (telemetry CUT) |
| Exporter link expiry | `exporter_expired_task.delete_old_s3_link` | 01:30 and 03:45 | No (exporter CUT) |
| API log cleanup | `cleanup_task.delete_api_logs` | 02:30 | No (API tokens CUT) |
| Webhook log cleanup | `cleanup_task.delete_webhook_logs` | 03:30 | No (webhooks CUT) |

## 3. Boot-time calls into CUT features (need stubs)

Boot sequence: `lib/wrappers/authentication-wrapper.tsx` (users/me, profile, settings, users/me/workspaces),
`instance-wrapper.tsx` (`/api/instances/`), `layouts/auth-layout/workspace-wrapper.tsx`,
`layouts/auth-layout/project-wrapper.tsx`, the top-nav unread-notification badge, and the home widgets. Only one
of these hits a CUT endpoint:

| Called from | Endpoint | Django behaviour | Suggested Go stub |
|---|---|---|---|
| `project-wrapper.tsx:111` → `fetchProjectIntakeState` (every project page) | `GET P/intake-state/` (`IntakeStateEndpoint`, `state/base.py:138`) | 200 `StateSerializer` of the triage state, or **404 `{"error":"Triage state not found"}`** when none exists | Ported in batch 7: project creation seeds a Triage state (it is in DEFAULT_STATES), so Django answers 200 for every project made through the app |

CUT data reached by kept boot calls (not endpoints, but the payload depends on them):

- `GET W/projects/` (boot) returns `intake_count` (COUNT over `intake_issues` with status pending). Return `0`, or keep the table.
- `GET W/projects/<pk>/` and `W/projects/details/` return `anchor` from `deploy_boards`. Return `null`, or keep the table.
- `GET W/search/` includes an `intake` bucket. Return `[]`.
- `PATCH W/projects/<pk>/` with `intake_view=true` creates an `Intake` row. Skip it, or keep the table.

Batch 6 returns `intake_count: 0` and `anchor: null` and skips the Intake row (see DEVIATIONS.md). The soft-delete
cascade still lists the CUT tables (`intakes`, `deploy_boards`, …); regenerate `internal/softdelete/relations_gen.go`
when they are pruned at cutover.

CUT endpoints the web only calls from feature pages (stub with 404, or leave unrouted). None run at boot:
project publish modal → `P/project-deploy-boards/`. Analytics page → `W/advance-analytics*`,
`P/advance-analytics*`, `W/project-stats/`. Settings → API tokens / webhooks / exports / imports / integrations.
Workspace drafts page → `W/draft-issues/`. Cover-image picker → `/api/unsplash/`. Editor AI menu →
`W/ai-assistant/`, `W/rephrase-grammar/`. Intake pages → `P/intake-issues/`, `P/inbox-issues/`,
`P/intake-work-items/…`. OAuth buttons → `/auth/{google,github,gitlab,gitea}/` (full-page redirects).

Web calls that have **no Django route at all** (404 today, so Go's 404 already matches): `W/dashboard/`,
`W/dashboard/<id>/`, `/api/dashboard/<id>/widgets/<id>/` (dashboard store, never triggered), `PATCH
W/sidebar-preferences/<key>/` (store action, no component calls it), `P/bulk-operation-issues/`,
`P/bulk-subscribe-issues/`, `P/issue-display-properties/`, `P/epics-user-properties/`, `P/views/<id>/issues/`,
`P/pages/<id>/move/`, `P/pages/<id>/versions/<id>/restore/`, `P/favorite-pages/` (GET), `P/archived-pages/`,
`P/cycles/<id>/cycle-progress/`, `W/active-cycles/`, `W/my-issues/`, `/api/release-notes/`, `/api/configs/`,
`DELETE P/issues/<id>/issue-relation/<id>/`, `PUT P/states/<id>/`, `AW/<entity_id>/bulk/` (workspace bulk asset),
and from apps/live `GET P/pages/<id>/mentions/`. Most are EE-only service methods left in the CE web.

## Appendix A: excluded endpoints

- **Already ported (7; `GET /api/instances/` lives in `plane/license/urls.py`):** `GET /api/instances/`, `GET /auth/get-csrf-token/`,
  `POST /auth/email-check/`, `POST /auth/sign-up/`, `POST /auth/sign-in/`, `POST /auth/sign-out/`,
  `GET /api/users/me/`.
- **CUT, space auth (17):** every `/auth/spaces/*` route.
- **CUT, OAuth (8):** `/auth/{google,github,gitlab,gitea}/` and `…/callback/`.
- **CUT, analytics (16):** `app/urls/analytic.py`. All routes there, including `W/project-stats/`, which the web uses
  only on the analytics overview page.
- **CUT, API tokens (8):** `app/urls/api.py` (`/api/users/api-tokens/…`).
- **CUT, exporter (2):** `W/export-issues/`.
- **CUT, external (3):** `/api/unsplash/`, `W/ai-assistant/`, `P/ai-assistant/`.
- **CUT, intake (22 + 1):** `app/urls/intake.py` (intakes, inboxes, intake-issues, inbox-issues,
  intake-work-items description versions) and `P/intake-state/` (state urls).
- **CUT, webhooks (10):** `app/urls/webhook.py`.
- **CUT, deploy boards / publish (5):** `P/project-deploy-boards/…`.
- **CUT, drafts (6):** `W/draft-issues/…`, `W/draft-to-issue/<draft_id>/`.
