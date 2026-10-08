package storage

import (
	"errors"
	"testing"
	"time"
)

func testClient() *Client {
	return New(Config{Endpoint: "https://account.r2.cloudflarestorage.com", AccessKeyID: "key", SecretAccessKey: "secret",
		Bucket: "uploads", Region: "auto", SignedURLExpiration: time.Hour})
}

func code(err error) string {
	var pe *PostError
	if errors.As(err, &pe) {
		return pe.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestPostPolicyRoundTrip(t *testing.T) {
	c := testClient()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	form, err := c.NewPost("https://api.example.com/api/assets/v2/upload/x/", "ws/abc-a.png", "image/png", 100, now)
	if err != nil {
		t.Fatal(err)
	}
	if form.Fields["x-amz-credential"] != "key/20261008/auto/s3/aws4_request" || form.Fields["key"] != "ws/abc-a.png" {
		t.Fatalf("fields %v", form.Fields)
	}
	fields := func(edit func(map[string]string)) map[string]string {
		f := map[string]string{}
		for k, v := range form.Fields {
			f[k] = v
		}
		if edit != nil {
			edit(f)
		}
		return f
	}
	p, err := c.CheckPost(fields(nil), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "ws/abc-a.png" || p.ContentType != "image/png" || p.MinSize != 1 || p.MaxSize != 100 {
		t.Fatalf("post %+v", p)
	}
	if code(p.CheckSize(0)) != "EntityTooSmall" || code(p.CheckSize(101)) != "EntityTooLarge" || p.CheckSize(100) != nil {
		t.Error("size range")
	}
	cases := map[string]struct {
		fields map[string]string
		at     time.Time
		want   string
	}{
		"expired":    {fields(nil), now.Add(2 * time.Hour), "AccessDenied"},
		"type":       {fields(func(f map[string]string) { f["Content-Type"] = "image/gif" }), now, "AccessDenied"},
		"key":        {fields(func(f map[string]string) { f["key"] = "other" }), now, "AccessDenied"},
		"extra":      {fields(func(f map[string]string) { f["acl"] = "public-read" }), now, "AccessDenied"},
		"ignored":    {fields(func(f map[string]string) { f["x-ignore-me"] = "1" }), now, ""},
		"signature":  {fields(func(f map[string]string) { f["x-amz-signature"] = "00" }), now, "SignatureDoesNotMatch"},
		"policy":     {fields(func(f map[string]string) { f["policy"] = "e30=" }), now, "SignatureDoesNotMatch"},
		"no policy":  {fields(func(f map[string]string) { delete(f, "policy") }), now, "InvalidArgument"},
		"credential": {fields(func(f map[string]string) { f["x-amz-credential"] = "other/x" }), now, "SignatureDoesNotMatch"},
	}
	for name, tc := range cases {
		_, err := c.CheckPost(tc.fields, tc.at)
		if got := code(err); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
	// Another secret signs differently.
	other := New(Config{Endpoint: "https://x", AccessKeyID: "key", SecretAccessKey: "other", Bucket: "uploads"})
	if _, err := other.CheckPost(fields(nil), now); code(err) != "SignatureDoesNotMatch" {
		t.Errorf("other secret: %v", err)
	}
}

func TestUnconfigured(t *testing.T) {
	if New(Config{Bucket: "uploads"}) != nil {
		t.Fatal("a client without credentials")
	}
	var c *Client
	if _, err := c.PresignGet(t.Context(), "k", "inline"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("presign: %v", err)
	}
	if _, err := c.NewPost("u", "k", "image/png", 1, time.Now()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("post: %v", err)
	}
}
