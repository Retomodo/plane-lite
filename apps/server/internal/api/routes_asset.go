package api

import (
	"context"
	"time"

	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/storage"
)

// PORTING.md section 17, plus the issue attachments of section 7. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerAssetJobs() {
	s := a.cfg.Storage
	a.storage = storage.New(storage.Config{Endpoint: s.Endpoint, AccessKeyID: s.AccessKeyID,
		SecretAccessKey: s.SecretAccessKey, Bucket: s.Bucket, Region: s.Region,
		SignedURLExpiration: time.Duration(s.SignedURLExpiration) * time.Second})
	if a.storage == nil {
		a.log.Warn("object storage is not configured: asset uploads and downloads will fail " +
			"(set AWS_S3_ENDPOINT_URL, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_S3_BUCKET_NAME)")
	}
	jobs.Register(a.jobs, func(ctx context.Context, j assetMetadataJob) error {
		if err := a.assetObjectMetadata(ctx, j); err != nil {
			a.log.Error("get_asset_object_metadata", "asset", j.AssetID, "err", err)
		}
		return nil
	})
	jobs.Register(a.jobs, func(ctx context.Context, j unuploadedAssetsJob) error {
		n, err := deleteUnuploadedAssets(ctx, a.db, a.cfg.UnuploadedAssetDeleteDays)
		if err != nil {
			a.log.Error("delete_unuploaded_file_asset", "err", err)
			return err
		}
		a.log.Info("delete_unuploaded_file_asset", "deleted", n)
		return nil
	})
}

func (a *API) registerAssetRoutes(rt *httpx.Router) {
	const (
		v2 = "/api/assets/v2/"
		aw = v2 + "workspaces/{slug}/"
		ap = aw + "projects/{project_id}/"
	)

	// plane/app/urls/asset.py. Variants marked unused in PORTING.md (POST
	// with an id, PATCH/DELETE without) answer 405.
	rt.Handle(v2+"user-assets/", httpx.Methods{"POST": a.createUserAsset})
	rt.Handle(v2+"user-assets/{asset_id}/", httpx.Methods{"PATCH": a.patchUserAsset, "DELETE": a.deleteUserAsset})
	rt.HandlePublic(v2+"static/{asset_id}/", httpx.Methods{"GET": a.anon(a.staticAsset)})

	rt.Handle(aw, httpx.Methods{"POST": a.allowWorkspace(anyRole, a.createWorkspaceAsset)})
	rt.Handle(aw+"{asset_id}/", httpx.Methods{
		"GET":    a.allowWorkspace(anyRole, a.getWorkspaceAsset),
		"PATCH":  a.allowWorkspace(anyRole, a.patchWorkspaceAsset),
		"DELETE": a.allowWorkspace(anyRole, a.deleteWorkspaceAsset),
	})
	rt.Handle(aw+"restore/{asset_id}/", httpx.Methods{"POST": a.allowWorkspace(anyRole, a.restoreAsset)})
	rt.Handle(aw+"check/{asset_id}/", httpx.Methods{"GET": a.allowWorkspace(anyRole, a.checkAsset)})
	rt.Handle(aw+"duplicate-assets/{asset_id}/", httpx.Methods{
		"POST": a.throttleAsset(a.allowWorkspace(anyRole, a.duplicateAsset)),
	})
	rt.Handle(aw+"download/{asset_id}/", httpx.Methods{"GET": a.allowWorkspace(anyRole, a.downloadWorkspaceAsset)})

	rt.Handle(ap, httpx.Methods{"POST": a.allowProject(anyRole, a.createProjectAsset)})
	rt.Handle(ap+"{pk}/", httpx.Methods{
		"GET":   a.allowProject(anyRole, a.getProjectAsset),
		"PATCH": a.allowProject(anyRole, a.patchProjectAsset),
	})
	// "<uuid:entity_id>/bulk/" and "download/<uuid:asset_id>/".
	rt.HandlePublic(ap+"{a}/{b}/", assetSubroute(false, func(c *httpx.Ctx) httpx.Methods {
		switch {
		case c.Param("a") == "download" && isUUIDParam(c, "b"):
			c.R.SetPathValue("asset_id", c.Param("b"))
			return httpx.Methods{"GET": a.allowProject(anyRole, a.downloadProjectAsset)}
		case c.Param("b") == "bulk" && isUUIDParam(c, "a"):
			c.R.SetPathValue("entity_id", c.Param("a"))
			return httpx.Methods{"POST": a.allowProject(anyRole, a.bulkAssets)}
		}
		return nil
	}))

	// plane/app/urls/issue.py: v2 issue attachments.
	att := ap + "issues/{issue_id}/attachments/"
	rt.Handle(att, httpx.Methods{
		"GET":  a.allowProject(anyRole, a.listIssueAttachments),
		"POST": a.allowProject(anyRole, a.createIssueAttachment),
	})
	rt.Handle(att+"{pk}/", httpx.Methods{
		"GET":    a.allowProject(anyRole, a.getIssueAttachment),
		"PATCH":  a.allowProject(anyRole, a.patchIssueAttachment),
		"DELETE": a.allowAttachmentDelete(a.deleteIssueAttachment),
	})

	// Legacy (pre-v2) asset URLs: only their DELETE and restore have callers.
	// "workspaces/file-assets/<uuid:workspace_id>/<str:asset_key>/" overlaps
	// every "workspaces/<slug>/<name>/<id>/" route in the mux, so they are
	// served from all-wildcard patterns, which every other route outranks.
	rt.HandlePublic(wsPrefix+"{workspace_id}/{asset_key}/", assetSubroute(false, func(c *httpx.Ctx) httpx.Methods {
		if c.Param("slug") != "file-assets" || !isUUIDParam(c, "workspace_id") {
			return nil
		}
		return httpx.Methods{"DELETE": a.legacyWorkspaceAsset(true)}
	}))
	rt.HandlePublic(wsPrefix+"{workspace_id}/{asset_key}/{action}/", assetSubroute(false, func(c *httpx.Ctx) httpx.Methods {
		if c.Param("slug") != "file-assets" || !isUUIDParam(c, "workspace_id") || c.Param("action") != "restore" {
			return nil
		}
		return httpx.Methods{"POST": a.legacyWorkspaceAsset(false)}
	}))
	rt.Handle("/api/users/file-assets/{asset_key}/", httpx.Methods{"DELETE": a.deleteLegacyUserAsset})

	// Go only: where upload_data sends the browser (asset_upload.go).
	rt.HandlePublic(v2+"upload/{asset_id}/", httpx.Methods{"POST": a.uploadAsset})
}
