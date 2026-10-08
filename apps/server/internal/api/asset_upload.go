package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/storage"
)

// Uploads. Django hands the browser an S3 presigned POST; Cloudflare R2
// has no POST uploads, so Go's upload_data points here instead (a
// deliberate deviation, see DEVIATIONS.md). The browser posts the same
// multipart form it would post to S3, the upload_data fields then "file".
// This endpoint checks the signed policy as S3 would (signature, expiry,
// key, Content-Type, size range) and writes the object with PutObject,
// answering like S3: 204, or an XML error.

// assetUploadMemory is how much of an upload is held in memory; larger
// files (FILE_SIZE_LIMIT allowing) spool to a temporary file.
const assetUploadMemory = 8 << 20

// uploadAsset is the Go stand-in for S3's POST Object.
func (a *API) uploadAsset(c *httpx.Ctx) error {
	id, err := c.UUIDParam("asset_id")
	if err != nil {
		return err
	}
	if a.storage == nil {
		return errAssetStorage
	}
	// Fields are small; the file is bounded by the policy's maximum below.
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, a.cfg.FileSizeLimit+1<<20)
	mr, err := c.R.MultipartReader()
	if err != nil {
		return s3Error(c, &storage.PostError{Status: http.StatusBadRequest, Code: "MalformedPOSTRequest",
			Message: "The body of your POST request is not well-formed multipart/form-data."})
	}
	fields := map[string]string{}
	var file io.Reader
	for file == nil {
		part, err := mr.NextPart()
		if err == io.EOF {
			return s3Error(c, &storage.PostError{Status: http.StatusBadRequest, Code: "InvalidArgument",
				Message: "POST requires exactly one file upload per request."})
		}
		if err != nil {
			return s3Error(c, &storage.PostError{Status: http.StatusBadRequest, Code: "MalformedPOSTRequest",
				Message: "The body of your POST request is not well-formed multipart/form-data."})
		}
		if part.FormName() == "file" {
			file = part
			break
		}
		v, err := io.ReadAll(io.LimitReader(part, 64<<10))
		if err != nil {
			return err
		}
		fields[part.FormName()] = string(v)
	}
	post, err := a.storage.CheckPost(fields, time.Now())
	var perr *storage.PostError
	if errors.As(err, &perr) {
		return s3Error(c, perr)
	}
	if err != nil {
		return err
	}
	// The policy names the object; it must be this asset's.
	var key string
	err = a.db.QueryRow(c.Context(), `SELECT asset FROM file_assets WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && key != post.Key {
		return s3Error(c, &storage.PostError{Status: http.StatusForbidden, Code: "AccessDenied",
			Message: "Invalid according to Policy: the key is not this asset's."})
	}
	if err != nil {
		return err
	}
	body, n, cleanup, err := spoolUpload(file, post.MaxSize)
	defer cleanup()
	if err != nil {
		return err
	}
	if perr := post.CheckSize(n); perr != nil {
		return s3Error(c, perr)
	}
	if err := a.storage.Put(c.Context(), post.Key, body, n, post.ContentType); err != nil {
		return err
	}
	return c.NoContent()
}

// spoolUpload reads at most max+1 bytes of r (one more than allowed, to
// detect too-large files) into memory or, past assetUploadMemory, a
// temporary file.
func spoolUpload(r io.Reader, max int64) (io.ReadSeeker, int64, func(), error) {
	limited := io.LimitReader(r, max+1)
	if max < assetUploadMemory {
		b, err := io.ReadAll(limited)
		return bytes.NewReader(b), int64(len(b)), func() {}, err
	}
	f, err := os.CreateTemp("", "plane-upload-*")
	if err != nil {
		return nil, 0, func() {}, err
	}
	cleanup := func() {
		f.Close()
		os.Remove(f.Name())
	}
	n, err := io.Copy(f, limited)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	return f, n, cleanup, err
}

// s3Error writes an S3 XML error document.
func s3Error(c *httpx.Ctx, e *storage.PostError) error {
	c.W.Header().Set("Content-Type", "application/xml")
	c.W.WriteHeader(e.Status)
	_, err := c.W.Write([]byte(e.XML()))
	return err
}
