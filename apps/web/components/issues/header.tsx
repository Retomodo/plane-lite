/**
 * Copyright (c) 2023-present Plane Software, Inc. and contributors
 * SPDX-License-Identifier: AGPL-3.0-only
 * See the LICENSE file for details.
 */

import { observer } from "mobx-react";
import { useParams } from "next/navigation";
// icons
// plane imports
import { EUserPermissions, EUserPermissionsLevel } from "@plane/constants";
import { useTranslation } from "@plane/i18n";
import { Button } from "@makeplane/propel/elements/button";
import { WorkItemsOutline } from "@makeplane/propel/icons";
import { Tooltip } from "@makeplane/propel/components/tooltip";
import { EIssuesStoreType } from "@plane/types";
import { Breadcrumbs } from "@plane/blocks/breadcrumb";
import { Header } from "@plane/blocks/layout";
// components
import { BreadcrumbLink } from "@/components/common/breadcrumb-link";
import { CountChip } from "@/components/common/count-chip";
// constants
import { HeaderFilters } from "@/components/issues/filters";
// helpers
// hooks
import { useCommandPalette } from "@/hooks/store/use-command-palette";
import { useIssues } from "@/hooks/store/use-issues";
import { useProject } from "@/hooks/store/use-project";
import { useUserPermissions } from "@/hooks/store/user";
import { usePlatformOS } from "@/hooks/use-platform-os";
// plane web imports
import { CommonProjectBreadcrumbs } from "@/components/breadcrumbs/common";
import { useProjectCrumbProps } from "@/components/breadcrumbs/use-project-crumb-props";

export const IssuesHeader = observer(function IssuesHeader() {
  // router
  const { workspaceSlug, projectId } = useParams();
  const projectCrumb = useProjectCrumbProps(workspaceSlug?.toString(), projectId?.toString());
  // store hooks
  const {
    issues: { getGroupIssueCount },
  } = useIssues(EIssuesStoreType.PROJECT);
  // i18n
  const { t } = useTranslation();

  const { currentProjectDetails, loader } = useProject();

  const { toggleCreateIssueModal } = useCommandPalette();
  const { allowPermissions } = useUserPermissions();
  const { isMobile } = usePlatformOS();

  const issuesCount = getGroupIssueCount(undefined, undefined, false);
  const canUserCreateIssue = allowPermissions(
    [EUserPermissions.ADMIN, EUserPermissions.MEMBER],
    EUserPermissionsLevel.PROJECT
  );

  return (
    <Header>
      <Header.LeftItem>
        <div className="flex items-center gap-2.5">
          <Breadcrumbs isLoading={loader === "init-loader"} className="flex-grow-0">
            <CommonProjectBreadcrumbs
              workspaceSlug={workspaceSlug?.toString()}
              projectId={projectId?.toString()}
              {...projectCrumb}
            />
            <Breadcrumbs.Item
              component={
                <BreadcrumbLink
                  label="Work Items"
                  href={`/${workspaceSlug}/projects/${projectId}/issues/`}
                  icon={<WorkItemsOutline className="h-4 w-4 text-tertiary" />}
                  isLast
                />
              }
              isLast
            />
          </Breadcrumbs>
          {issuesCount && issuesCount > 0 ? (
            <Tooltip
              label={`There are ${issuesCount} ${issuesCount > 1 ? "work items" : "work item"} in this project`}
              layout="stacked"
              side="bottom"
              disabled={isMobile}
            >
              <CountChip count={issuesCount} />
            </Tooltip>
          ) : null}
        </div>
      </Header.LeftItem>
      <Header.RightItem>
        <div className="hidden gap-2 md:flex">
          <HeaderFilters
            projectId={projectId}
            currentProjectDetails={currentProjectDetails}
            workspaceSlug={workspaceSlug}
          />
        </div>
        {canUserCreateIssue && (
          <Button
            variant="primary"
            size="md"
            stretch="auto"
            type="button"
            onClick={() => {
              toggleCreateIssueModal(true, EIssuesStoreType.PROJECT);
            }}
          >
            <div className="block sm:hidden">{t("issue.label", { count: 1 })}</div>
            <div className="hidden sm:block">{t("issue.add.label")}</div>
          </Button>
        )}
      </Header.RightItem>
    </Header>
  );
});
