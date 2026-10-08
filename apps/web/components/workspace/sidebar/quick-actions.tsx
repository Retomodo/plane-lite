/**
 * Copyright (c) 2023-present Plane Software, Inc. and contributors
 * SPDX-License-Identifier: AGPL-3.0-only
 * See the LICENSE file for details.
 */

import { observer } from "mobx-react";
// plane imports
import { EUserPermissions, EUserPermissionsLevel } from "@plane/constants";
import { useTranslation } from "@plane/i18n";
import { AddWorkItemOutline } from "@makeplane/propel/icons";
// components
import { SidebarAddButton } from "@/components/sidebar/add-button";
// hooks
import { useCommandPalette } from "@/hooks/store/use-command-palette";
import { useProject } from "@/hooks/store/use-project";
import { useUserPermissions } from "@/hooks/store/user";

export const SidebarQuickActions = observer(function SidebarQuickActions() {
  const { t } = useTranslation();
  // store hooks
  const { toggleCreateIssueModal } = useCommandPalette();
  const { joinedProjectIds } = useProject();
  const { allowPermissions } = useUserPermissions();
  // derived values
  const canCreateIssue = allowPermissions(
    [EUserPermissions.ADMIN, EUserPermissions.MEMBER],
    EUserPermissionsLevel.WORKSPACE
  );
  const disabled = joinedProjectIds.length === 0 || !canCreateIssue;

  return (
    <div className="flex cursor-pointer items-center justify-between gap-2">
      <SidebarAddButton
        label={
          <>
            <AddWorkItemOutline className="size-4" />
            <span className="max-w-[145px] truncate text-13 font-medium">{t("sidebar.new_work_item")}</span>
          </>
        }
        onClick={() => toggleCreateIssueModal(true)}
        disabled={disabled}
      />
    </div>
  );
});
