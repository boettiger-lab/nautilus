package main

// Admin API — protected by the X-Admin-Key header.
//
// Routes:
//   GET    /admin/keys                           → list all keys
//   POST   /admin/keys                           → create a key
//   DELETE /admin/keys/{keyId}                   → delete a key
//   GET    /admin/keys/{keyId}                   → get key details + permissions
//   PUT    /admin/keys/{keyId}/buckets/{bucket}  → set bucket permissions
//   DELETE /admin/keys/{keyId}/buckets/{bucket}  → revoke bucket access

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

type Admin struct {
	cfg   Config
	store *Store
}

func NewAdmin(cfg Config, store *Store) *Admin {
	return &Admin{cfg: cfg, store: store}
}

func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Admin-Key") != a.cfg.AdminSecret {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Strip the /admin prefix and route on what remains.
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	path = strings.TrimSuffix(path, "/")

	switch {
	case path == "/keys" && r.Method == http.MethodGet:
		a.listKeys(w, r)

	case path == "/keys" && r.Method == http.MethodPost:
		a.createKey(w, r)

	case strings.HasPrefix(path, "/keys/") && strings.Contains(path[len("/keys/"):], "/buckets/"):
		a.handleBucketPermission(w, r, path)

	case strings.HasPrefix(path, "/keys/"):
		keyID := strings.TrimPrefix(path, "/keys/")
		switch r.Method {
		case http.MethodGet:
			a.getKey(w, r, keyID)
		case http.MethodDelete:
			a.deleteKey(w, r, keyID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}

	default:
		http.NotFound(w, r)
	}
}

// ── Request / response types ───────────────────────────────────────────────

type CreateKeyRequest struct {
	Description string `json:"description"`
	// ExpiresIn is a Go duration string, e.g. "24h", "168h", "720h".
	// Omit for a key that never expires.
	ExpiresIn string `json:"expires_in,omitempty"`
}

type KeyResponse struct {
	AccessKeyID string             `json:"access_key_id"`
	SecretKey   string             `json:"secret_key,omitempty"` // only on create
	Description string             `json:"description"`
	CreatedAt   time.Time          `json:"created_at"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
	Active      bool               `json:"active"`
	Buckets     []BucketPermission `json:"buckets,omitempty"`
}

type BucketPermissionRequest struct {
	AllowRead  bool `json:"allow_read"`
	AllowWrite bool `json:"allow_write"`
	AllowList  bool `json:"allow_list"`
}

// ── Handlers ───────────────────────────────────────────────────────────────

func (a *Admin) listKeys(w http.ResponseWriter, _ *http.Request) {
	keys, err := a.store.ListKeys()
	if err != nil {
		log.Printf("listKeys: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var resp []KeyResponse
	for _, k := range keys {
		perms, _ := a.store.ListBucketPermissions(k.AccessKeyID)
		resp = append(resp, KeyResponse{
			AccessKeyID: k.AccessKeyID,
			Description: k.Description,
			CreatedAt:   k.CreatedAt,
			ExpiresAt:   k.ExpiresAt,
			Active:      k.Active,
			Buckets:     perms,
		})
	}
	jsonOK(w, resp)
}

func (a *Admin) getKey(w http.ResponseWriter, _ *http.Request, keyID string) {
	k, err := a.store.GetKey(keyID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if k == nil {
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}
	perms, _ := a.store.ListBucketPermissions(keyID)
	jsonOK(w, KeyResponse{
		AccessKeyID: k.AccessKeyID,
		Description: k.Description,
		CreatedAt:   k.CreatedAt,
		ExpiresAt:   k.ExpiresAt,
		Active:      k.Active,
		Buckets:     perms,
	})
}

func (a *Admin) createKey(w http.ResponseWriter, r *http.Request) {
	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Generate credentials — access key is 20 hex chars prefixed "S3GW",
	// secret is 40 hex chars (160 bits of entropy).
	accessKeyID := "S3GW" + randomHex(8)
	secretKey := randomHex(20)

	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			http.Error(w, "invalid expires_in — use Go duration syntax (e.g. 24h, 168h)", http.StatusBadRequest)
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	k := Key{
		AccessKeyID: accessKeyID,
		SecretKey:   secretKey,
		Description: req.Description,
		ExpiresAt:   expiresAt,
		Active:      true,
	}
	if err := a.store.CreateKey(k); err != nil {
		log.Printf("createKey: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	jsonOK(w, KeyResponse{
		AccessKeyID: accessKeyID,
		SecretKey:   secretKey, // only returned once — not stored in plaintext retrieval
		Description: req.Description,
		ExpiresAt:   expiresAt,
		Active:      true,
	})
}

func (a *Admin) deleteKey(w http.ResponseWriter, _ *http.Request, keyID string) {
	if err := a.store.DeleteKey(keyID); err != nil {
		log.Printf("deleteKey %s: %v", keyID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) handleBucketPermission(w http.ResponseWriter, r *http.Request, path string) {
	// path format: /keys/{keyId}/buckets/{bucket}
	after := strings.TrimPrefix(path, "/keys/")
	keyID, bucket, ok := strings.Cut(after, "/buckets/")
	if !ok || keyID == "" || bucket == "" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req BucketPermissionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		err := a.store.SetBucketPermission(BucketPermission{
			AccessKeyID: keyID,
			Bucket:      bucket,
			AllowRead:   req.AllowRead,
			AllowWrite:  req.AllowWrite,
			AllowList:   req.AllowList,
		})
		if err != nil {
			log.Printf("setBucketPerm %s/%s: %v", keyID, bucket, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		if err := a.store.DeleteBucketPermission(keyID, bucket); err != nil {
			log.Printf("deleteBucketPerm %s/%s: %v", keyID, bucket, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("json encode: %v", err)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
