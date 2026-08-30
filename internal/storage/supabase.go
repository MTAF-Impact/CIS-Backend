package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cis/cis-backend/internal/config"
)

// Supabase stores policy documents in a Supabase Storage bucket over its REST
// API, authenticating with the service-role key.
//
// The service-role key bypasses row-level security, so it must only ever live
// in server-side environment variables — never in a client bundle.
type Supabase struct {
	baseURL    string
	serviceKey string
	bucket     string
	signTTL    time.Duration
	client     *http.Client
}

// NewSupabase constructs the Supabase Storage driver.
func NewSupabase(cfg config.StorageConfig) (*Supabase, error) {
	if cfg.SupabaseURL == "" || cfg.SupabaseServiceKey == "" {
		return nil, fmt.Errorf("SUPABASE_URL and SUPABASE_SERVICE_ROLE_KEY are required for the supabase storage driver")
	}
	return &Supabase{
		baseURL:    strings.TrimRight(cfg.SupabaseURL, "/"),
		serviceKey: cfg.SupabaseServiceKey,
		bucket:     cfg.SupabaseBucket,
		signTTL:    cfg.SignedURLTTL,
		// No client-side timeout: US40 allows arbitrarily large policy
		// documents, and a fixed deadline would truncate a slow large upload.
		// Cancellation is driven by the request context instead.
		client: &http.Client{},
	}, nil
}

// Driver names the implementation.
func (s *Supabase) Driver() string { return "supabase" }

func (s *Supabase) objectURL(action, path string) string {
	return fmt.Sprintf("%s/storage/v1/object/%s/%s/%s",
		s.baseURL, action, url.PathEscape(s.bucket), encodePath(path))
}

func (s *Supabase) setAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+s.serviceKey)
	req.Header.Set("apikey", s.serviceKey)
}

// Upload streams the file into the bucket, overwriting any object already at
// the same path.
func (s *Supabase) Upload(ctx context.Context, path string, r io.Reader, size int64, mimeType string) (*Object, error) {
	endpoint := fmt.Sprintf("%s/storage/v1/object/%s/%s",
		s.baseURL, url.PathEscape(s.bucket), encodePath(path))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, r)
	if err != nil {
		return nil, fmt.Errorf("build upload request: %w", err)
	}
	s.setAuth(req)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("x-upsert", "true")
	if size > 0 {
		req.ContentLength = size
		req.Header.Set("Content-Length", strconv.FormatInt(size, 10))
	}

	res, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload to supabase storage: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("supabase storage upload failed: %s", describeError(res))
	}
	return &Object{Path: path, MimeType: mimeType, Size: size}, nil
}

// SignedURL asks Supabase for a time-limited download link.
func (s *Supabase) SignedURL(ctx context.Context, path string) (string, bool, error) {
	endpoint := s.objectURL("sign", path)

	body, err := json.Marshal(map[string]any{"expiresIn": int(s.signTTL.Seconds())})
	if err != nil {
		return "", false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", false, fmt.Errorf("build sign request: %w", err)
	}
	s.setAuth(req)
	req.Header.Set("Content-Type", "application/json")

	res, err := s.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("sign supabase object: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", false, fmt.Errorf("supabase storage sign failed: %s", describeError(res))
	}

	var payload struct {
		SignedURL string `json:"signedURL"`
		SignedUrl string `json:"signedUrl"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", false, fmt.Errorf("decode sign response: %w", err)
	}

	signed := payload.SignedURL
	if signed == "" {
		signed = payload.SignedUrl
	}
	if signed == "" {
		return "", false, fmt.Errorf("supabase returned an empty signed URL")
	}
	// Supabase returns a storage-relative path; make it absolute.
	if strings.HasPrefix(signed, "/") {
		return s.baseURL + "/storage/v1" + signed, true, nil
	}
	return s.baseURL + "/storage/v1/" + signed, true, nil
}

// Download streams an object out of the bucket.
func (s *Supabase) Download(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL("authenticated", path), nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	s.setAuth(req)

	res, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download from supabase storage: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, fmt.Errorf("supabase storage download failed: %s", describeError(res))
	}
	return res.Body, nil
}

// Delete removes an object from the bucket.
func (s *Supabase) Delete(ctx context.Context, path string) error {
	endpoint := fmt.Sprintf("%s/storage/v1/object/%s/%s",
		s.baseURL, url.PathEscape(s.bucket), encodePath(path))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	s.setAuth(req)

	res, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete from supabase storage: %w", err)
	}
	defer res.Body.Close()

	// A already-missing object is not an error for our purposes.
	if res.StatusCode == http.StatusNotFound {
		return nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("supabase storage delete failed: %s", describeError(res))
	}
	return nil
}

// encodePath percent-encodes each path segment while preserving the separators.
func encodePath(path string) string {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// describeError summarizes a failed Supabase response without leaking the
// service-role key or a huge body into logs.
func describeError(res *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	message := strings.TrimSpace(string(body))
	if message == "" {
		return res.Status
	}
	return fmt.Sprintf("%s: %s", res.Status, message)
}
