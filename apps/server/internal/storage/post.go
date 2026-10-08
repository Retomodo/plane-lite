package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The browser uploads with a form POST carrying a signed policy, as it would
// to S3's presigned POST. R2 doesn't accept POST uploads, so the policy is
// addressed to the API's own upload endpoint instead, which checks it here
// (with S3's rules and error codes) and writes the file with PutObject.
//
// The policy is a real SigV4 POST policy over the bucket's secret key, the
// same document boto3's generate_presigned_post builds for Plane's
// S3Storage, so the fields the API returns have the shape Django's do.

const (
	postAlgorithm = "AWS4-HMAC-SHA256"
	amzDateFormat = "20060102T150405Z"
)

// PostForm is upload_data: where to POST and the fields to send before the
// file.
type PostForm struct {
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

// NewPost is S3Storage.generate_presigned_post(key, contentType, maxSize):
// a policy pinning the bucket, the key, the Content-Type and a size between
// 1 and maxSize bytes, valid for the configured expiration. uploadURL is
// where the form goes (the API's upload endpoint).
func (c *Client) NewPost(uploadURL, key, contentType string, maxSize int64, now time.Time) (PostForm, error) {
	if c == nil {
		return PostForm{}, ErrNotConfigured
	}
	now = now.UTC()
	amzDate := now.Format(amzDateFormat)
	credential := c.cfg.AccessKeyID + "/" + c.scope(now)
	policy := map[string]any{
		"expiration": now.Add(c.cfg.SignedURLExpiration).Format("2006-01-02T15:04:05Z"),
		"conditions": []any{
			map[string]string{"bucket": c.cfg.Bucket},
			[]any{"content-length-range", 1, maxSize},
			map[string]string{"Content-Type": contentType},
			map[string]string{"key": key},
			map[string]string{"x-amz-algorithm": postAlgorithm},
			map[string]string{"x-amz-credential": credential},
			map[string]string{"x-amz-date": amzDate},
		},
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return PostForm{}, err
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	return PostForm{URL: uploadURL, Fields: map[string]string{
		"Content-Type":     contentType,
		"key":              key,
		"x-amz-algorithm":  postAlgorithm,
		"x-amz-credential": credential,
		"x-amz-date":       amzDate,
		"policy":           encoded,
		"x-amz-signature":  c.sign(now, encoded),
	}}, nil
}

func (c *Client) scope(t time.Time) string {
	return t.Format("20060102") + "/" + c.cfg.Region + "/s3/aws4_request"
}

// sign is the SigV4 signature of a POST policy dated t.
func (c *Client) sign(t time.Time, policy string) string {
	mac := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	k := mac([]byte("AWS4"+c.cfg.SecretAccessKey), t.Format("20060102"))
	k = mac(k, c.cfg.Region)
	k = mac(k, "s3")
	k = mac(k, "aws4_request")
	return hex.EncodeToString(mac(k, policy))
}

// PostError is an S3-style upload rejection.
type PostError struct {
	Status  int
	Code    string
	Message string
}

func (e *PostError) Error() string { return e.Code + ": " + e.Message }

// XML is the error document S3 answers with.
func (e *PostError) XML() string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n<Error><Code>" + e.Code + "</Code><Message>" +
		esc.Replace(e.Message) + "</Message></Error>"
}

func denied(msg string) *PostError {
	return &PostError{Status: http.StatusForbidden, Code: "AccessDenied", Message: msg}
}

// Post is an upload whose policy checked out: the object to write and the
// size the file must have.
type Post struct {
	Key         string
	ContentType string
	MinSize     int64
	MaxSize     int64
}

// CheckSize applies the policy's content-length-range to the file's size.
func (p *Post) CheckSize(n int64) *PostError {
	if n < p.MinSize {
		return &PostError{Status: http.StatusBadRequest, Code: "EntityTooSmall",
			Message: "Your proposed upload is smaller than the minimum allowed object size."}
	}
	if n > p.MaxSize {
		return &PostError{Status: http.StatusBadRequest, Code: "EntityTooLarge",
			Message: "Your proposed upload exceeds the maximum allowed object size."}
	}
	return nil
}

// CheckPost verifies the form fields of an upload (everything before the
// file) the way S3 checks a POST: signature, expiry, every condition, and
// no field the policy doesn't cover.
func (c *Client) CheckPost(fields map[string]string, now time.Time) (*Post, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	get := func(name string) (string, bool) {
		for k, v := range fields {
			if strings.EqualFold(k, name) {
				return v, true
			}
		}
		return "", false
	}
	policy, ok := get("policy")
	if !ok {
		return nil, &PostError{Status: http.StatusBadRequest, Code: "InvalidArgument",
			Message: "Bucket POST must contain a field named 'policy'."}
	}
	sig, _ := get("x-amz-signature")
	algo, _ := get("x-amz-algorithm")
	credential, _ := get("x-amz-credential")
	date, _ := get("x-amz-date")
	t, err := time.Parse(amzDateFormat, date)
	if algo != postAlgorithm || err != nil || credential != c.cfg.AccessKeyID+"/"+c.scope(t) ||
		!hmac.Equal([]byte(sig), []byte(c.sign(t, policy))) {
		return nil, &PostError{Status: http.StatusForbidden, Code: "SignatureDoesNotMatch",
			Message: "The request signature we calculated does not match the signature you provided."}
	}
	raw, err := base64.StdEncoding.DecodeString(policy)
	if err != nil {
		return nil, denied("Invalid Policy: Invalid Simple-Condition.")
	}
	var doc struct {
		Expiration string            `json:"expiration"`
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, denied("Invalid Policy: Invalid JSON.")
	}
	exp, err := time.Parse(time.RFC3339, doc.Expiration)
	if err != nil || !now.Before(exp) {
		return nil, denied("Invalid according to Policy: Policy expired.")
	}
	p := &Post{MinSize: 0, MaxSize: -1}
	covered := map[string]bool{}
	for _, cond := range doc.Conditions {
		var eq map[string]string
		if json.Unmarshal(cond, &eq) == nil && len(eq) == 1 {
			for k, want := range eq {
				covered[strings.ToLower(k)] = true
				if strings.EqualFold(k, "bucket") {
					if want != c.cfg.Bucket {
						return nil, denied("Invalid according to Policy: Policy Condition failed: [\"eq\", \"$bucket\", \"" + want + "\"]")
					}
					continue
				}
				if got, _ := get(k); got != want {
					return nil, denied(fmt.Sprintf("Invalid according to Policy: Policy Condition failed: [\"eq\", \"$%s\", %q]", k, want))
				}
			}
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(cond, &arr); err != nil || len(arr) != 3 {
			return nil, denied("Invalid Policy: Invalid Simple-Condition.")
		}
		var op string
		_ = json.Unmarshal(arr[0], &op)
		switch strings.ToLower(op) {
		case "content-length-range":
			if json.Unmarshal(arr[1], &p.MinSize) != nil || json.Unmarshal(arr[2], &p.MaxSize) != nil {
				return nil, denied("Invalid Policy: Invalid content-length-range.")
			}
		case "eq", "starts-with":
			var name, want string
			if json.Unmarshal(arr[1], &name) != nil || json.Unmarshal(arr[2], &want) != nil {
				return nil, denied("Invalid Policy: Invalid Simple-Condition.")
			}
			name = strings.TrimPrefix(name, "$")
			covered[strings.ToLower(name)] = true
			got, _ := get(name)
			if op == "eq" && got != want || op != "eq" && !strings.HasPrefix(got, want) {
				return nil, denied(fmt.Sprintf("Invalid according to Policy: Policy Condition failed: [%q, \"$%s\", %q]", op, name, want))
			}
		default:
			return nil, denied("Invalid Policy: Invalid Simple-Condition.")
		}
	}
	for k := range fields {
		lk := strings.ToLower(k)
		if lk == "policy" || lk == "x-amz-signature" || strings.HasPrefix(lk, "x-ignore-") || covered[lk] {
			continue
		}
		return nil, denied("Invalid according to Policy: Extra input fields: " + k)
	}
	p.Key, _ = get("key")
	p.ContentType, _ = get("Content-Type")
	if p.Key == "" || p.MaxSize < 0 {
		return nil, denied("Invalid according to Policy: Policy Condition failed: [\"eq\", \"$key\", \"\"]")
	}
	return p, nil
}
