/**
 * Copyright (c) 2023-present Plane Software, Inc. and contributors
 * SPDX-License-Identifier: AGPL-3.0-only
 * See the LICENSE file for details.
 */

import React, { useEffect } from "react";
import { observer } from "mobx-react";
import type { Control } from "react-hook-form";
import { Controller } from "react-hook-form";
// plane imports
import { ETabIndices } from "@plane/constants";
import type { EditorRefApi } from "@plane/editor";
import { useTranslation } from "@plane/i18n";
import { setToast } from "@plane/blocks/toast";
import type { TIssue } from "@plane/types";
import { EFileAssetType } from "@plane/types";
import { Loader } from "@plane/blocks/skeleton";
import { getDescriptionPlaceholderI18n, getTabIndex } from "@plane/utils";
// components
import { RichTextEditor } from "@/components/editor/rich-text";
// helpers
// hooks
import { useEditorAsset } from "@/hooks/store/use-editor-asset";
import { useWorkspace } from "@/hooks/store/use-workspace";
import useKeypress from "@/hooks/use-keypress";
import { usePlatformOS } from "@/hooks/use-platform-os";
// plane web services
import { WorkspaceService } from "@/services/workspace.service";
const workspaceService = new WorkspaceService();

type TIssueDescriptionEditorProps = {
  control: Control<TIssue>;
  issueId: string | undefined;
  descriptionHtmlData: string | undefined;
  editorRef: React.MutableRefObject<EditorRefApi | null>;
  submitBtnRef: React.MutableRefObject<HTMLButtonElement | null>;
  workspaceSlug: string;
  projectId: string | null;
  handleFormChange: () => void;
  handleDescriptionHTMLDataChange: (descriptionHtmlData: string) => void;
  onAssetUpload: (assetId: string) => void;
  onClose: () => void;
};

export const IssueDescriptionEditor = observer(function IssueDescriptionEditor(props: TIssueDescriptionEditorProps) {
  const {
    control,
    issueId,
    descriptionHtmlData,
    editorRef,
    submitBtnRef,
    workspaceSlug,
    projectId,
    handleFormChange,
    handleDescriptionHTMLDataChange,
    onAssetUpload,
    onClose,
  } = props;
  // i18n
  const { t } = useTranslation();
  // store hooks
  const { getWorkspaceBySlug } = useWorkspace();
  const workspaceId = getWorkspaceBySlug(workspaceSlug?.toString())?.id ?? "";
  const { uploadEditorAsset, duplicateEditorAsset } = useEditorAsset();
  // platform
  const { isMobile } = usePlatformOS();

  const { getIndex } = getTabIndex(ETabIndices.ISSUE_FORM, isMobile);

  useEffect(() => {
    if (descriptionHtmlData) handleDescriptionHTMLDataChange(descriptionHtmlData);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [descriptionHtmlData]);

  const handleKeyDown = (event: KeyboardEvent) => {
    if (editorRef.current?.isEditorReadyToDiscard()) {
      onClose();
    } else {
      setToast({
        type: "error",
        title: "Error!",
        message: "Editor is still processing changes. Please wait before proceeding.",
      });
      event.preventDefault(); // Prevent default action if editor is not ready to discard
    }
  };

  useKeypress("Escape", handleKeyDown);

  return (
    <div className="relative rounded-lg border-[0.5px] border-subtle-1 bg-layer-2">
      {descriptionHtmlData === undefined || !projectId ? (
        <Loader className="max-h-64 min-h-[120px] space-y-2 overflow-hidden rounded-md border border-subtle p-3 py-2 pt-3">
          <Loader.Item width="100%" height="26px" />
          <div className="flex items-center gap-2">
            <Loader.Item width="26px" height="26px" />
            <Loader.Item width="400px" height="26px" />
          </div>
          <div className="flex items-center gap-2">
            <Loader.Item width="26px" height="26px" />
            <Loader.Item width="400px" height="26px" />
          </div>
          <Loader.Item width="80%" height="26px" />
          <div className="flex items-center gap-2">
            <Loader.Item width="50%" height="26px" />
          </div>
          <div className="border-0.5 absolute right-3.5 bottom-2 z-10 flex items-center gap-2">
            <Loader.Item width="100px" height="26px" />
            <Loader.Item width="50px" height="26px" />
          </div>
        </Loader>
      ) : (
        <>
          <Controller
            name="description_html"
            control={control}
            render={({ field: { value, onChange } }) => (
              <RichTextEditor
                editable
                id="issue-modal-editor"
                initialValue={value ?? ""}
                value={descriptionHtmlData}
                workspaceSlug={workspaceSlug?.toString()}
                workspaceId={workspaceId}
                projectId={projectId}
                onChange={(_description: object, description_html: string) => {
                  onChange(description_html);
                  handleFormChange();
                }}
                onEnterKeyPress={() => submitBtnRef?.current?.click()}
                ref={editorRef}
                tabIndex={getIndex("description_html")}
                placeholder={(isFocused, description) => t(getDescriptionPlaceholderI18n(isFocused, description))}
                searchMentionCallback={async (payload) =>
                  await workspaceService.searchEntity(workspaceSlug?.toString() ?? "", {
                    ...payload,
                    project_id: projectId?.toString() ?? "",
                  })
                }
                containerClassName="pt-3 min-h-[120px]"
                uploadFile={async (blockId, file) => {
                  try {
                    const { asset_id } = await uploadEditorAsset({
                      blockId,
                      data: {
                        entity_identifier: issueId ?? "",
                        entity_type: EFileAssetType.ISSUE_DESCRIPTION,
                      },
                      file,
                      projectId,
                      workspaceSlug,
                    });
                    onAssetUpload(asset_id);
                    return asset_id;
                  } catch (error) {
                    console.log("Error in uploading issue asset:", error);
                    throw new Error("Asset upload failed. Please try again later.", { cause: error });
                  }
                }}
                duplicateFile={async (assetId: string) => {
                  try {
                    const { asset_id } = await duplicateEditorAsset({
                      assetId,
                      entityId: issueId,
                      entityType: EFileAssetType.ISSUE_DESCRIPTION,
                      projectId,
                      workspaceSlug,
                    });
                    onAssetUpload(asset_id);
                    return asset_id;
                  } catch {
                    throw new Error("Asset duplication failed. Please try again later.");
                  }
                }}
              />
            )}
          />
        </>
      )}
    </div>
  );
});
