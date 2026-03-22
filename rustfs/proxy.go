package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Proxy handles incoming S3 API requests, verifies credentials and permissions,
// re-signs with the upstream master credentials, and forwards to Ceph S3.
type Proxy struct {
	cfg    Config
	store  *Store
	client *http.Client
	signer *v4.Signer
	creds  aws.Credentials
}

func NewProxy(cfg Config, store *Store) *Proxy {
	return &Proxy{
		cfg:   cfg,
		store: store,
		client: &http.Client{
			Timeout: 10 * time.Minute,
			// Do not follow redirects — pass them straight back to the client.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		signer: v4.NewSigner(),
		creds: aws.Credentials{
			AccessKeyID:     cfg.UpstreamKey,
			SecretAccessKey: cfg.UpstreamSecret,
		},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ── 1. Extract bucket from path (/bucket/key…) ─────────────────────────
	bucket, err := extractBucket(r.URL.Path)
	if err != nil {
		s3Error(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}

	// ── 2. Extract access key ID from Authorization header ─────────────────
	accessKeyID, err := ExtractAccessKeyID(r)
	if err != nil {
		s3Error(w, "InvalidAccessKeyId", "Missing or invalid Authorization header", http.StatusForbidden)
		return
	}

	// ── 3. Look up key; reject if absent, inactive, or expired ─────────────
	key, err := p.store.GetKey(accessKeyID)
	if err != nil {
		log.Printf("store error for key %s: %v", accessKeyID, err)
		s3Error(w, "InternalError", "Internal server error", http.StatusInternalServerError)
		return
	}
	if key == nil || !key.Active {
		s3Error(w, "InvalidAccessKeyId", "The access key ID does not exist", http.StatusForbidden)
		return
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		s3Error(w, "ExpiredToken", "The access key has expired", http.StatusForbidden)
		return
	}

	// ── 4. Verify SigV4 signature ──────────────────────────────────────────
	if err := VerifySigV4(r, key.SecretKey); err != nil {
		log.Printf("SigV4 failure for key %s: %v", accessKeyID, err)
		s3Error(w, "SignatureDoesNotMatch", "The request signature does not match", http.StatusForbidden)
		return
	}

	// ── 5. Determine required permissions from method + path ───────────────
	needRead, needWrite, needList := requiredPermissions(r)

	// ── 6. Check bucket-level access ──────────────────────────────────────
	allowed, err := p.store.CheckAccess(accessKeyID, bucket, needRead, needWrite, needList)
	if err != nil {
		log.Printf("access check error for key %s / bucket %s: %v", accessKeyID, bucket, err)
		s3Error(w, "InternalError", "Internal server error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		s3Error(w, "AccessDenied", "Access Denied", http.StatusForbidden)
		return
	}

	// ── 7. Build and sign upstream request ─────────────────────────────────
	upstreamURL := p.buildUpstreamURL(r)

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		log.Printf("failed to build upstream request: %v", err)
		s3Error(w, "InternalError", "Internal server error", http.StatusInternalServerError)
		return
	}
	copyClientHeaders(upReq, r)

	// Sign with master credentials. We use UNSIGNED-PAYLOAD so we don't have
	// to buffer the entire request body — Ceph accepts this.
	if err := p.signer.SignHTTP(
		context.Background(), p.creds, upReq,
		"UNSIGNED-PAYLOAD", "s3", p.cfg.UpstreamRegion, time.Now(),
	); err != nil {
		log.Printf("signing error: %v", err)
		s3Error(w, "InternalError", "Internal server error", http.StatusInternalServerError)
		return
	}

	// ── 8. Execute and stream response ─────────────────────────────────────
	resp, err := p.client.Do(upReq)
	if err != nil {
		log.Printf("upstream request failed: %v", err)
		s3Error(w, "ServiceUnavailable", "Upstream S3 unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("response copy error: %v", err)
	}
}

// extractBucket returns the bucket name from an S3 path-style URL (/bucket/key…).
func extractBucket(path string) (string, error) {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return "", fmt.Errorf("path is missing bucket name")
	}
	bucket, _, _ := strings.Cut(path, "/")
	if bucket == "" {
		return "", fmt.Errorf("empty bucket name in path")
	}
	return bucket, nil
}

// requiredPermissions maps an HTTP method + URL onto the read/write/list flags.
func requiredPermissions(r *http.Request) (needRead, needWrite, needList bool) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	_, key, _ := strings.Cut(path, "/")
	hasKey := key != ""
	q := r.URL.Query()

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		// Bucket-level GETs and explicit listing params → list permission.
		isListing := !hasKey ||
			q.Has("list-type") || q.Has("delimiter") ||
			q.Has("uploads") || q.Has("versions")
		if isListing {
			needList = true
		} else {
			needRead = true
		}
	case http.MethodPut, http.MethodPost, http.MethodDelete:
		needWrite = true
	}
	return
}

// buildUpstreamURL replaces the host in the incoming URL with the upstream
// Ceph endpoint, preserving path and query string.
func (p *Proxy) buildUpstreamURL(r *http.Request) string {
	upstream, _ := url.Parse(p.cfg.UpstreamEndpoint)
	upstream.Path = r.URL.Path
	upstream.RawQuery = r.URL.RawQuery
	return upstream.String()
}

// copyClientHeaders copies safe headers from the client request to the
// upstream request, excluding auth headers that will be replaced by re-signing.
func copyClientHeaders(dst, src *http.Request) {
	skip := map[string]bool{
		"Authorization":          true,
		"X-Amz-Security-Token":  true,
		"X-Amz-Date":            true,
		"X-Amz-Content-Sha256":  true,
	}
	for k, vals := range src.Header {
		if skip[k] {
			continue
		}
		for _, v := range vals {
			dst.Header.Add(k, v)
		}
	}
	// Content-Length is set by Go's http.Client from r.Body; don't duplicate.
	dst.ContentLength = src.ContentLength
}

// s3Error writes an S3-compatible XML error response.
func s3Error(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<Error><Code>%s</Code><Message>%s</Message></Error>`, code, message)
}
