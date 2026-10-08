# Web trim (ROADMAP.md section 4)

Trims `apps/web` so that no reachable UI calls a CUT endpoint (PORTING.md section 3 and Appendix A)
or links to a removed page. Branch `port/web-trim`. `check:types`, `check:lint` and `build` pass for
`web`. `check:types` also passes for `admin`, `space`, `@plane/types` and `@plane/constants`, because
this change edits those shared packages. The `web` lint warning count went from 538 to 474, and
`--max-warnings` went down by the same 64, from 11957 to 11893.

Paths below are relative to `apps/web` unless they start with `packages/`.

## 1. Removed, by feature

### Intake

- Routes and pages: `P/intake` (`app/(all)/[workspaceSlug]/(projects)/projects/(detail)/[projectId]/intake/`),
  the `settings/projects/:projectId/features/intake` page, and the legacy redirect `projects/:projectId/inbox`
  (`app/routes/redirects/core/inbox.tsx`).
- Entry points: the Intake tab in project navigation (`components/navigation/use-navigation-items.ts`,
  `tab-navigation-utils.ts`), the Intake item and `intake_count` badge in the sidebar project list
  (`components/workspace/sidebar/project-navigation.tsx`), the Intake toggle in the project feature list
  (`components/project/settings/features-list.tsx`, shown after a project is created), the
  `features_intake` settings tab (`packages/constants/src/settings/project.ts`,
  `packages/types/src/settings.ts`), the `nav_project_intake` Power-K command (`gk`), the
  `is_intake` redirect on `browse/[workItem]`, and the intake branch of the notifications pane
  (`components/workspace-notifications/root.tsx`, which embedded `InboxContentRoot`).
- Code: `components/inbox/`, `components/projects/settings/intake/`, `components/dropdowns/intake-state/`,
  `components/ui/loader/layouts/project-inbox/`, `store/inbox/`, `services/inbox/`,
  `hooks/store/use-project-inbox.ts`, `use-inbox-issues.ts`, the `projectInbox` root store, and the
  `useIntakeHeaderMenuItems` stub.
- Boot call: `layouts/auth-layout/project-wrapper.tsx` no longer fetches `P/intake-state/`.
  `fetchProjectIntakeState` and the intake-state maps are gone from `store/state.store.ts`,
  `getIntakeState` is gone from `services/project/project-state.service.ts`, and the
  `PROJECT_INTAKE_STATE` key is gone from `packages/constants/src/fetch-keys.ts`.
- `components/issues/attachment/{root,attachment-detail,attachments-list,attachment-upload*,index}`: only
  the intake detail view used these. Work item detail uses `attachment-item-list` / `attachment-list-*`,
  which stay.
- `components/issues/issue-detail/label/root.tsx`: removed the `isInboxIssue` prop.

### Publish / deploy boards / space app

- The "Publish project" menu items in `components/navigation/project-actions-menu.tsx` (with
  `use-project-actions.ts` and `tab-navigation-root.tsx`) and in
  `components/workspace/sidebar/projects-list-item.tsx`.
- The "Public" link to the space app in `components/issues/header.tsx`.
- `components/project/publish-project/`, `store/project/project-publish.store.ts` (the `projectRoot.publish`
  store), `hooks/store/use-project-publish.ts`, `services/project/project-publish.service.ts`.
- View publishing was already a no-op CE stub. Removed it too: `components/views/publish/`, the
  `useViewPublish` hook in `components/views/quick-actions.tsx`, and the "Live" badge with its
  `getPublishViewLink` link in `components/views/view-list-item-action.tsx`.

### Analytics

- Routes: `:workspaceSlug/analytics/:tabId` and the `:workspaceSlug/analytics` redirect.
- The workspace sidebar Analytics item (`packages/constants/src/workspace.ts`,
  `WORKSPACE_SIDEBAR_DYNAMIC_NAVIGATION_ITEMS`) and the `nav_workspace_analytics` Power-K command
  (`ga`).
- The work item "Analytics" modal (`advance-analytics*`), together with its buttons in
  `components/issues/filters.tsx` (project work items header, which also lost its `canUserCreateIssue`
  prop), the cycle and module detail headers, and the mobile headers for issues, cycles and modules
  (under `app/(all)/[workspaceSlug]/(projects)/projects/(detail)/[projectId]/`).
- `components/analytics/`, `store/analytics.store.ts`, `hooks/store/use-analytics.ts`,
  `services/analytics.service.ts`, `components/chart/utils.ts` (used only by analytics), and
  `project-stats` (`getProjectAnalyticsCount` / `fetchProjectAnalyticsCount` / `projectAnalyticsCountMap`
  in `services/project/project.service.ts` and `store/project/project.store.ts`).
- Kept: the cycle and module sidebars (`components/{cycles,modules}/analytics-sidebar/`), which use
  `P/cycles/<id>/progress/` and `P/cycles/<id>/analytics`.

### Webhooks, API tokens, imports/exports, integrations

- Routes: `settings/exports`, `settings/webhooks`, `settings/webhooks/:webhookId`, the
  `:workspaceSlug/settings/api-tokens` redirect, and the routeless `settings/integrations` page.
- Tabs: `export` and `webhooks` came out of `WORKSPACE_SETTINGS`, and `api-tokens` came out of
  `PROFILE_SETTINGS` (`packages/constants/src/settings/{workspace,profile}.ts`). The tab unions in
  `packages/types/src/settings.ts` shrank to match. The "Developer" categories are now empty, so the
  sidebars skip them.
- Code: `components/exporter/`, `components/web-hooks/`, `components/api-token/`, `components/integration/`,
  `components/project/integration-card.tsx`, `components/settings/profile/content/pages/api-tokens.tsx`,
  `components/ui/loader/settings/{api-token,web-hook,import-and-export,integration}.tsx`,
  `store/workspace/{webhook,api-token}.store.ts`, `hooks/store/use-webhook.ts`,
  `hooks/use-integration-popup.tsx`, `services/webhook.service.ts`, `services/integrations/`,
  `services/project/project-export.service.ts`.

### AI

- The page editor AI menu: `aiHandler` is no longer passed in `components/pages/editor/editor-body.tsx`,
  and `components/pages/editor/ai/` is deleted.
- In the create/edit work item modal: the "AI" popover and the "I'm feeling lucky" button, which call
  `ai-assistant` (`components/issues/issue-modal/components/description-editor.tsx`, `form.tsx`), and
  `components/core/modals/gpt-assistant-popover.tsx`.
- `services/ai.service.ts`.

### Unsplash

- `components/core/image-picker-popover.tsx` now has only the Images (static covers) and Upload tabs.
  Before this change its `useSWR` called `/api/unsplash/` every time the picker mounted, even with
  the tab hidden. The unused `control` prop is gone from the picker's three callers.
- `getUnsplashImages` and the `UnSplashImage*` types are gone from `services/file.service.ts`.

### Drafts

- Route `:workspaceSlug/drafts`, the sidebar Drafts item (`WORKSPACE_SIDEBAR_STATIC_NAVIGATION_ITEMS.drafts`),
  the Drafts entry in "Customize navigation" and in the personal navigation preferences
  (`TPersonalNavigationItemKey` in `packages/types/src/navigation-preferences.ts`,
  `hooks/use-navigation-preferences.ts`), and the `nav_workspace_drafts` Power-K command (`gj`).
- The create-issue modal: `DraftIssueLayout` and `ConfirmIssueDiscard` ("Save to Drafts"), and the
  `isDraft` / `moveToIssue` / `withDraftIssueWrapper` props with their code paths in `issue-modal/{base,form,modal}.tsx`
  and `components/{description-editor,default-properties}.tsx`.
- `components/workspace/sidebar/quick-actions.tsx`: removed the localStorage `draftedIssue` modal. Nothing
  ever opened it.
- `components/issues/workspace-draft/`, `store/issue/workspace-draft/`, `hooks/store/workspace-draft/`,
  `services/issue/workspace_draft.service.ts`. `EIssuesStoreType.WORKSPACE_DRAFT` is gone from
  `packages/types/src/issues/issue.ts`, along with its cases in `hooks/store/use-issues.ts`,
  `hooks/use-issues-actions.tsx`, `hooks/use-group-dragndrop.ts` and `components/issues/issue-layouts/`.

### OAuth

- `components/account/auth-forms/auth-root.tsx` no longer renders `OAuthOptions`. `hooks/oauth/` and
  the Google/GitHub/GitLab/Gitea logos are deleted. `github-black.png` stays because the "Star us"
  link uses it.

### God-mode admin

- The "Enter god mode" item in `components/workspace/sidebar/user-menu-root.tsx`. It was hard-wired
  off, but it linked to `GOD_MODE_URL`.
- `components/instance/not-ready-view.tsx`: the "Get started" button that linked to god-mode is gone,
  and the text now points at the server configuration. Section 2 explains why the view stays.

### Other dead code removed

These files were already unreachable before this change and pointed at cut routes:
`components/workspace/sidebar/{user-menu,user-menu-item,workspace-menu,workspace-menu-item,workspace-menu-header}.tsx`.
Assets used only by cut features: `app/assets/empty-state/{analytics,intake}/`,
`app/assets/empty-state/{api-token,web-hook}.svg`, `app/assets/empty-state/disabled-feature/intake-*`,
`app/assets/services/`, `app/assets/logos/github-square.png`. `inboxId` / `webhookId` are gone from
`store/router.store.ts`.

## 2. Hidden or kept rather than removed

- The shared packages keep the cut code, because `space`, `admin` and `live` build against them and
  `web` no longer imports any of it:
  - `packages/services`: `ai/`, `developer/{api-token,webhook}.service.ts`, `intake/`.
  - `packages/constants`: `intake.ts`, `analytics/`, `workspace-drafts.ts`, `ai.ts`,
    `EProjectFeatureKey.INTAKE`, `GOD_MODE_URL`, `ADMIN_BASE_*`, `API_TOKENS_LIST`.
  - `packages/types`: intake, workspace-draft, webhook, api-token, `TProjectAnalyticsCount`, and the
    `EFileAssetType.DRAFT_ISSUE_DESCRIPTION` value.
  - `packages/blocks`: `IntakeStateSelect`, `OAuthOptions`, and the `intake` empty-state illustration.
  - `packages/utils`: `intake`.
  - `packages/i18n`: the locale strings for every cut feature.
- `InstanceNotReady` stays as a defensive fallback. Go marks the instance set up at boot, so
  `is_setup_done === false` never happens.
- `TProject` keeps `inbox_view`, `intake_count` and `anchor`, because Go still returns them (`0` and
  `null`). `components/issues/issue-detail/issue-activity/root.tsx` still reads `!!project.anchor`
  for `showAccessSpecifier`, which is always false now.

## 3. Remaining unreachable references in apps/web

A grep for the cut URLs (`intake-issues`, `inbox-issues`, `project-deploy-boards`, `advance-analytics`,
`project-stats`, `api-tokens`, `webhooks`, `export-issues`, `importers`, `unsplash`, `ai-assistant`,
`rephrase-grammar`, `draft-issues`, `god-mode`, `/auth/{google,...}`, `intake-state`) over `apps/web`
finds no service call left. The remaining hits are:

- `helpers/cover-image.helper.ts`: recognizes existing `unsplash.com` cover URLs for display. It makes
  no API call.
- `helpers/authentication.helper.tsx`: messages for the `ADMIN_*` error codes link to `/admin`. Only
  the admin sign-in endpoints produce these codes, and the web never calls them.
- Display only, with no reachable data in a fresh plane-lite: issue-activity `inbox.tsx` /
  `activity-list.tsx` "intake" case, `common/activity/{helper,user}.tsx` (`intake_view`, the "-intake"
  bot name), `issues/peek-overview/properties.tsx` ("-intake" bot name), the `isIntakeIssue` props on
  the issue-activity components (now always false), and `notification.is_inbox_issue`. The notification
  card now treats a missing value as false, so a work item peek still opens if Go omits the field.
- `store/theme.store.ts`: `workspaceAnalyticsSidebarCollapsed` is dead UI state.
- `components/workspace/billing/comparison/plans.tsx`: marketing copy mentions importers,
  integrations and analytics. It is static text with no API call.
- `packages/constants/src/workspace.ts` `RESTRICTED_URLS`: `god-mode`, `importers` and `integrations`
  are reserved workspace slugs, which is harmless.
- `services/app_config.service.ts`: `/api/configs/`. This was dead before this change and is out of
  scope.

## 4. Uncertain or entangled

- **Create-issue discard confirmation.** Upstream wrapped the modal in `DraftIssueLayout`, whose
  "Save this draft?" dialog offered only Discard or Save to Drafts. That wrapper and dialog are gone.
  `issue-modal/base.tsx` now shows a "Discard changes?" `ConfirmDialog` from `@plane/blocks/dialog`,
  with Cancel and Discard, when a new work item is closed with unsaved changes. The prompt uses the old
  dialog's test for unsaved changes: the form only reports changes once the title or description has
  content, and empty values, the project and priority "none" are ignored. An untouched form and the
  edit flow close without asking, as upstream did. The new `common` i18n keys `discard_changes_title`
  and `discard_changes_description` are translated in every locale.
- **Triage state.** The web no longer loads `P/intake-state/`. The Go states list already leaves out
  `triage` (`internal/api/state.go`), so the Triage state that project creation seeds stays out of
  the UI.
- **Notifications.** An intake notification used to open the intake detail pane. It now goes through
  the normal work item peek. No such notifications can be created any more.
