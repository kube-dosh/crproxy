package registry

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crproxy/internal/auth"
	"crproxy/internal/cas"
	"crproxy/internal/oci"
	"crproxy/internal/store"
)

const (
	testAdminPassword  = "admin-password-123"
	testViewerPassword = "viewer-password-123"
)

type testEnv struct {
	server *httptest.Server
	store  *store.Store
	db     *sql.DB
	auth   *auth.Authenticator
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	casStore, err := cas.New(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	metadata := store.New(db)
	adminHash, err := auth.HashPassword(testAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := metadata.CreateUser(t.Context(), "admin", adminHash, "admin")
	if err != nil {
		t.Fatal(err)
	}
	viewerHash, err := auth.HashPassword(testViewerPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.CreateUser(t.Context(), "viewer", viewerHash, "viewer"); err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "crproxy", "crproxy", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := &auth.Authenticator{
		Store:     metadata,
		Signer:    signer,
		Realm:     "http://registry.test",
		Service:   "crproxy",
		DummyHash: auth.DummyPasswordHash(),
	}
	handler := &Handler{
		Store:            metadata,
		CAS:              casStore,
		Auth:             authenticator,
		MaxManifestBytes: 4 << 20,
		MaxBlobBytes:     1 << 20,
		UploadTTL:        time.Hour,
	}
	tokenHandler := &TokenHandler{Auth: authenticator}
	mux := http.NewServeMux()
	mux.Handle("/v2/token", tokenHandler)
	mux.Handle("/v2/", handler)
	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
		_ = db.Close()
	})
	_ = admin
	return &testEnv{server: server, store: metadata, db: db, auth: authenticator}
}

type testResponse struct {
	status int
	header http.Header
	body   []byte
}

func (e *testEnv) request(t *testing.T, method, path string, body []byte, headers map[string]string) testResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, e.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return testResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: raw}
}

func (e *testEnv) basicRequest(t *testing.T, method, path string, body []byte, user, password string, headers map[string]string) testResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, e.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.SetBasicAuth(user, password)
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return testResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: raw}
}

func (e *testEnv) token(t *testing.T, user, password, scope string) (string, testResponse) {
	t.Helper()
	query := url.Values{"service": {"crproxy"}}
	if scope != "" {
		query.Set("scope", scope)
	}
	resp := e.basicRequest(t, http.MethodGet, "/v2/token?"+query.Encode(), nil, user, password, nil)
	if resp.status != http.StatusOK {
		return "", resp
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Token, resp
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func digestOf(t *testing.T, data []byte) oci.Digest {
	t.Helper()
	digest, err := oci.FromBytes("sha256", data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestRegistryPushPullDiscoveryAndDelete(t *testing.T) {
	env := newTestEnv(t)

	base := env.request(t, http.MethodGet, "/v2/", nil, nil)
	if base.status != http.StatusUnauthorized || !strings.Contains(base.header.Get("WWW-Authenticate"), `realm="http://registry.test/v2/token"`) {
		t.Fatalf("unexpected unauthenticated response: status=%d challenge=%q", base.status, base.header.Get("WWW-Authenticate"))
	}

	token, tokenResp := env.token(t, "admin", testAdminPassword, "repository:demo/app:pull,push")
	if tokenResp.status != http.StatusOK || token == "" {
		t.Fatalf("token request failed: status=%d body=%s", tokenResp.status, tokenResp.body)
	}

	config := []byte("{}")
	configDigest := digestOf(t, config)
	upload := env.request(t, http.MethodPost, "/v2/demo/app/blobs/uploads/?digest="+configDigest.String(), config, bearer(token))
	if upload.status != http.StatusCreated || upload.header.Get("Docker-Content-Digest") != configDigest.String() {
		t.Fatalf("monolithic upload failed: status=%d headers=%v body=%s", upload.status, upload.header, upload.body)
	}

	layer := []byte("hello layer")
	layerDigest := digestOf(t, layer)
	start := env.request(t, http.MethodPost, "/v2/demo/app/blobs/uploads/", nil, bearer(token))
	if start.status != http.StatusAccepted {
		t.Fatalf("start upload failed: status=%d body=%s", start.status, start.body)
	}
	location := start.header.Get("Location")
	if location == "" || start.header.Get("Docker-Upload-UUID") == "" {
		t.Fatalf("upload start missing identifiers: headers=%v", start.header)
	}
	patch := env.request(t, http.MethodPatch, location, layer, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/octet-stream",
		"Content-Range": "0-10",
	})
	if patch.status != http.StatusAccepted || patch.header.Get("Range") != "0-10" {
		t.Fatalf("patch failed: status=%d headers=%v body=%s", patch.status, patch.header, patch.body)
	}
	status := env.request(t, http.MethodGet, location, nil, bearer(token))
	if status.status != http.StatusNoContent || status.header.Get("Range") != "0-10" {
		t.Fatalf("upload status failed: status=%d headers=%v", status.status, status.header)
	}
	complete := env.request(t, http.MethodPut, location+"?digest="+layerDigest.String(), nil, bearer(token))
	if complete.status != http.StatusCreated || complete.header.Get("Docker-Content-Digest") != layerDigest.String() {
		t.Fatalf("complete upload failed: status=%d headers=%v body=%s", complete.status, complete.header, complete.body)
	}

	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + configDigest.String() + `","size":2},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"` + layerDigest.String() + `","size":11}]}`)
	manifestDigest := digestOf(t, manifest)
	pushManifest := env.request(t, http.MethodPut, "/v2/demo/app/manifests/v1", manifest, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  oci.MediaTypeOCIManifest,
	})
	if pushManifest.status != http.StatusCreated || pushManifest.header.Get("Docker-Content-Digest") != manifestDigest.String() {
		t.Fatalf("manifest push failed: status=%d headers=%v body=%s", pushManifest.status, pushManifest.header, pushManifest.body)
	}

	pull := env.request(t, http.MethodGet, "/v2/demo/app/manifests/v1", nil, bearer(token))
	if pull.status != http.StatusOK || !bytes.Equal(pull.body, manifest) || pull.header.Get("Content-Type") != oci.MediaTypeOCIManifest {
		t.Fatalf("manifest pull failed: status=%d headers=%v body=%s", pull.status, pull.header, pull.body)
	}
	head := env.request(t, http.MethodHead, "/v2/demo/app/blobs/"+layerDigest.String(), nil, bearer(token))
	if head.status != http.StatusOK || head.header.Get("Content-Length") != "11" {
		t.Fatalf("blob head failed: status=%d headers=%v", head.status, head.header)
	}
	ranged := env.request(t, http.MethodGet, "/v2/demo/app/blobs/"+layerDigest.String(), nil, map[string]string{
		"Authorization": "Bearer " + token,
		"Range":         "bytes=0-4",
	})
	if ranged.status != http.StatusPartialContent || string(ranged.body) != "hello" {
		t.Fatalf("range read failed: status=%d headers=%v body=%q", ranged.status, ranged.header, ranged.body)
	}

	tags := env.request(t, http.MethodGet, "/v2/demo/app/tags/list", nil, bearer(token))
	if tags.status != http.StatusOK || string(tags.body) != "{\"name\":\"demo/app\",\"tags\":[\"v1\"]}\n" {
		t.Fatalf("tags failed: status=%d body=%s", tags.status, tags.body)
	}
	if catalog := env.request(t, http.MethodGet, "/v2/_catalog", nil, bearer(token)); catalog.status != http.StatusForbidden {
		t.Fatalf("JWT catalog should be forbidden, got %d", catalog.status)
	}
	catalog := env.basicRequest(t, http.MethodGet, "/v2/_catalog", nil, "admin", testAdminPassword, nil)
	if catalog.status != http.StatusOK || !strings.Contains(string(catalog.body), "demo/app") {
		t.Fatalf("catalog failed: status=%d body=%s", catalog.status, catalog.body)
	}
	emptyReferrers := env.request(t, http.MethodGet, "/v2/demo/app/referrers/"+manifestDigest.String(), nil, bearer(token))
	if emptyReferrers.status != http.StatusOK || !strings.Contains(string(emptyReferrers.body), `"manifests":[]`) {
		t.Fatalf("empty referrers failed: status=%d body=%s", emptyReferrers.status, emptyReferrers.body)
	}

	artifact := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.example.sbom+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + configDigest.String() + `","size":2},"layers":[{"mediaType":"application/vnd.oci.empty.v1+json","digest":"` + configDigest.String() + `","size":2}],"subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + manifestDigest.String() + `","size":` + jsonNumber(len(manifest)) + `}}`)
	artifactDigest := digestOf(t, artifact)
	artifactPush := env.request(t, http.MethodPut, "/v2/demo/app/manifests/"+artifactDigest.String(), artifact, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  oci.MediaTypeOCIManifest,
	})
	if artifactPush.status != http.StatusCreated || artifactPush.header.Get("OCI-Subject") != manifestDigest.String() {
		t.Fatalf("artifact push failed: status=%d headers=%v body=%s", artifactPush.status, artifactPush.header, artifactPush.body)
	}
	referrers := env.request(t, http.MethodGet, "/v2/demo/app/referrers/"+manifestDigest.String(), nil, bearer(token))
	if referrers.status != http.StatusOK || !strings.Contains(string(referrers.body), artifactDigest.String()) || referrers.header.Get("Content-Type") != oci.MediaTypeOCIIndex {
		t.Fatalf("referrers failed: status=%d headers=%v body=%s", referrers.status, referrers.header, referrers.body)
	}
	filtered := env.request(t, http.MethodGet, "/v2/demo/app/referrers/"+manifestDigest.String()+"?artifactType=application/vnd.example.missing", nil, bearer(token))
	if filtered.status != http.StatusOK || !strings.Contains(string(filtered.body), `"manifests":[]`) || filtered.header.Get("OCI-Filters-Applied") != "artifactType" {
		t.Fatalf("filtered referrers failed: status=%d headers=%v body=%s", filtered.status, filtered.header, filtered.body)
	}

	mount := env.basicRequest(t, http.MethodPost, "/v2/demo/target/blobs/uploads/?mount="+layerDigest.String()+"&from=demo/app", nil, "admin", testAdminPassword, nil)
	if mount.status != http.StatusCreated {
		t.Fatalf("mount failed: status=%d body=%s", mount.status, mount.body)
	}
	mounted := env.basicRequest(t, http.MethodHead, "/v2/demo/target/blobs/"+layerDigest.String(), nil, "admin", testAdminPassword, nil)
	if mounted.status != http.StatusOK {
		t.Fatalf("mounted blob unavailable: status=%d body=%s", mounted.status, mounted.body)
	}

	protected := env.basicRequest(t, http.MethodDelete, "/v2/demo/app/blobs/"+configDigest.String(), nil, "admin", testAdminPassword, nil)
	if protected.status != http.StatusConflict || !strings.Contains(string(protected.body), "BLOB_REFERENCED") {
		t.Fatalf("referenced blob delete should conflict: status=%d body=%s", protected.status, protected.body)
	}
	tagDelete := env.basicRequest(t, http.MethodDelete, "/v2/demo/app/manifests/v1", nil, "admin", testAdminPassword, nil)
	if tagDelete.status != http.StatusAccepted {
		t.Fatalf("tag delete failed: status=%d body=%s", tagDelete.status, tagDelete.body)
	}
	if byTag := env.request(t, http.MethodGet, "/v2/demo/app/manifests/v1", nil, bearer(token)); byTag.status != http.StatusNotFound {
		t.Fatalf("tag should be gone, got %d", byTag.status)
	}
	if byDigest := env.request(t, http.MethodGet, "/v2/demo/app/manifests/"+manifestDigest.String(), nil, bearer(token)); byDigest.status != http.StatusOK {
		t.Fatalf("manifest digest should remain after tag delete, got %d", byDigest.status)
	}
	for _, digest := range []string{artifactDigest.String(), manifestDigest.String()} {
		deleted := env.basicRequest(t, http.MethodDelete, "/v2/demo/app/manifests/"+digest, nil, "admin", testAdminPassword, nil)
		if deleted.status != http.StatusAccepted {
			t.Fatalf("manifest delete %s failed: status=%d body=%s", digest, deleted.status, deleted.body)
		}
	}
	if deletedBlob := env.basicRequest(t, http.MethodDelete, "/v2/demo/app/blobs/"+configDigest.String(), nil, "admin", testAdminPassword, nil); deletedBlob.status != http.StatusAccepted {
		t.Fatalf("unreferenced blob delete failed: status=%d body=%s", deletedBlob.status, deletedBlob.body)
	}
}

func jsonNumber(value int) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestUploadErrorsCancelAndOwnership(t *testing.T) {
	env := newTestEnv(t)
	start := env.basicRequest(t, http.MethodPost, "/v2/demo/upload/blobs/uploads/", nil, "admin", testAdminPassword, nil)
	if start.status != http.StatusAccepted {
		t.Fatalf("start upload failed: status=%d body=%s", start.status, start.body)
	}
	location := start.header.Get("Location")
	badRange := env.basicRequest(t, http.MethodPatch, location, []byte("abc"), "admin", testAdminPassword, map[string]string{"Content-Range": "1-3"})
	if badRange.status != http.StatusRequestedRangeNotSatisfiable || badRange.header.Get("Range") != "0-0" {
		t.Fatalf("bad range should fail with 416: status=%d headers=%v", badRange.status, badRange.header)
	}
	owned := env.basicRequest(t, http.MethodPatch, location, []byte("abc"), "viewer", testViewerPassword, nil)
	if owned.status != http.StatusForbidden {
		t.Fatalf("viewer should not be allowed to push, got %d", owned.status)
	}
	patch := env.basicRequest(t, http.MethodPatch, location, []byte("abc"), "admin", testAdminPassword, nil)
	if patch.status != http.StatusAccepted || patch.header.Get("Range") != "0-2" {
		t.Fatalf("patch failed: status=%d headers=%v", patch.status, patch.header)
	}
	wrongDigest := digestOf(t, []byte("different"))
	badDigest := env.basicRequest(t, http.MethodPut, location+"?digest="+wrongDigest.String(), nil, "admin", testAdminPassword, nil)
	if badDigest.status != http.StatusBadRequest || !strings.Contains(string(badDigest.body), "DIGEST_INVALID") {
		t.Fatalf("wrong digest should fail: status=%d body=%s", badDigest.status, badDigest.body)
	}
	if status := env.basicRequest(t, http.MethodGet, location, nil, "admin", testAdminPassword, nil); status.status != http.StatusNoContent {
		t.Fatalf("upload should be resumable after digest error, got %d", status.status)
	}
	correctDigest := digestOf(t, []byte("abc"))
	if complete := env.basicRequest(t, http.MethodPut, location+"?digest="+correctDigest.String(), nil, "admin", testAdminPassword, nil); complete.status != http.StatusCreated {
		t.Fatalf("upload completion failed: status=%d body=%s", complete.status, complete.body)
	}

	cancelStart := env.basicRequest(t, http.MethodPost, "/v2/demo/cancel/blobs/uploads/", nil, "admin", testAdminPassword, nil)
	cancelLocation := cancelStart.header.Get("Location")
	if cancel := env.basicRequest(t, http.MethodDelete, cancelLocation, nil, "admin", testAdminPassword, nil); cancel.status != http.StatusNoContent {
		t.Fatalf("upload cancel failed: status=%d body=%s", cancel.status, cancel.body)
	}
	if missing := env.basicRequest(t, http.MethodGet, cancelLocation, nil, "admin", testAdminPassword, nil); missing.status != http.StatusNotFound {
		t.Fatalf("cancelled upload should be unknown, got %d", missing.status)
	}

	oversized := bytes.Repeat([]byte("x"), (1<<20)+1)
	tooLarge := env.basicRequest(t, http.MethodPost, "/v2/demo/large/blobs/uploads/?digest="+digestOf(t, oversized).String(), oversized, "admin", testAdminPassword, nil)
	if tooLarge.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload should fail with 413, got %d: %s", tooLarge.status, tooLarge.body)
	}
}

func TestUploadStreamingLimitPreservesResumableData(t *testing.T) {
	env := newTestEnv(t)
	start := env.basicRequest(t, http.MethodPost, "/v2/demo/stream/blobs/uploads/", nil, "admin", testAdminPassword, nil)
	if start.status != http.StatusAccepted {
		t.Fatalf("start upload failed: status=%d body=%s", start.status, start.body)
	}
	location := start.header.Get("Location")
	first := env.basicRequest(t, http.MethodPatch, location, []byte("abc"), "admin", testAdminPassword, map[string]string{"Content-Range": "0-2"})
	if first.status != http.StatusAccepted || first.header.Get("Range") != "0-2" {
		t.Fatalf("first chunk failed: status=%d headers=%v body=%s", first.status, first.header, first.body)
	}

	oversized := bytes.Repeat([]byte("x"), (1<<20)-2)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, env.server.URL+location, io.NopCloser(bytes.NewReader(oversized)))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.SetBasicAuth("admin", testAdminPassword)
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("streaming oversized chunk should fail with 413, got %d: %s", resp.StatusCode, raw)
	}
	status := env.basicRequest(t, http.MethodGet, location, nil, "admin", testAdminPassword, nil)
	if status.status != http.StatusNoContent || status.header.Get("Range") != "0-2" {
		t.Fatalf("oversized chunk must not corrupt accepted data: status=%d headers=%v body=%s", status.status, status.header, status.body)
	}
	complete := env.basicRequest(t, http.MethodPut, location+"?digest="+digestOf(t, []byte("abc")).String(), nil, "admin", testAdminPassword, nil)
	if complete.status != http.StatusCreated {
		t.Fatalf("upload should remain resumable after streaming limit error: status=%d body=%s", complete.status, complete.body)
	}
}

func TestTokenAuthorizationBoundaries(t *testing.T) {
	env := newTestEnv(t)
	if _, resp := env.token(t, "viewer", testViewerPassword, "repository:demo/app:push"); resp.status != http.StatusForbidden {
		t.Fatalf("viewer push scope should be forbidden, got %d body=%s", resp.status, resp.body)
	}
	viewerToken, resp := env.token(t, "viewer", testViewerPassword, "repository:demo/app:pull")
	if resp.status != http.StatusOK || viewerToken == "" {
		t.Fatalf("viewer pull token failed: status=%d body=%s", resp.status, resp.body)
	}
	if push := env.request(t, http.MethodPost, "/v2/demo/app/blobs/uploads/", nil, bearer(viewerToken)); push.status != http.StatusForbidden {
		t.Fatalf("viewer JWT push should be forbidden, got %d", push.status)
	}
	scopedToken, _ := env.token(t, "admin", testAdminPassword, "repository:demo/app:pull")
	if other := env.request(t, http.MethodGet, "/v2/other/app/tags/list", nil, bearer(scopedToken)); other.status != http.StatusForbidden {
		t.Fatalf("JWT must not grant another repository, got %d", other.status)
	}
	badService := env.basicRequest(t, http.MethodGet, "/v2/token?service=wrong&scope=repository:demo/app:pull", nil, "admin", testAdminPassword, nil)
	if badService.status != http.StatusBadRequest {
		t.Fatalf("wrong service should fail, got %d", badService.status)
	}
	badAccount := env.basicRequest(t, http.MethodGet, "/v2/token?service=crproxy&account=viewer&scope=repository:demo/app:pull", nil, "admin", testAdminPassword, nil)
	if badAccount.status != http.StatusUnauthorized {
		t.Fatalf("account mismatch should fail, got %d", badAccount.status)
	}
	badAction := env.basicRequest(t, http.MethodGet, "/v2/token?service=crproxy&scope=repository:demo/app:admin", nil, "admin", testAdminPassword, nil)
	if badAction.status != http.StatusBadRequest {
		t.Fatalf("admin action in registry token scope should fail, got %d", badAction.status)
	}

	uploadStart := env.basicRequest(t, http.MethodPost, "/v2/conformance/upload/blobs/uploads/", nil, "admin", testAdminPassword, nil)
	if uploadStart.status != http.StatusAccepted {
		t.Fatalf("start upload failed: status=%d body=%s", uploadStart.status, uploadStart.body)
	}
	uploadLocation := uploadStart.header.Get("Location")
	pullOnly, _ := env.token(t, "admin", testAdminPassword, "repository:conformance/upload:pull")
	if status := env.request(t, http.MethodGet, uploadLocation, nil, bearer(pullOnly)); status.status != http.StatusNoContent {
		t.Fatalf("pull-scoped token should read upload status: status=%d body=%s", status.status, status.body)
	}
	deleteOnly, _ := env.token(t, "admin", testAdminPassword, "repository:conformance/upload:delete")
	if cancel := env.request(t, http.MethodDelete, uploadLocation, nil, bearer(deleteOnly)); cancel.status != http.StatusNoContent {
		t.Fatalf("delete-scoped token should cancel upload: status=%d body=%s", cancel.status, cancel.body)
	}

	subject := digestOf(t, []byte("subject"))
	unknownReferrers, _ := env.token(t, "admin", testAdminPassword, "repository:empty/repo:pull")
	if response := env.request(t, http.MethodGet, "/v2/empty/repo/referrers/"+subject.String(), nil, bearer(unknownReferrers)); response.status != http.StatusOK || response.header.Get("Content-Type") != oci.MediaTypeOCIIndex {
		t.Fatalf("unknown repository referrers should return an empty index: status=%d headers=%v body=%s", response.status, response.header, response.body)
	}
	invalidManifest := env.basicRequest(t, http.MethodPut, "/v2/conformance/upload/manifests/sha256:baddigest", []byte(`{}`), "admin", testAdminPassword, map[string]string{"Content-Type": oci.MediaTypeOCIManifest})
	if invalidManifest.status != http.StatusBadRequest {
		t.Fatalf("invalid manifest digest should return 400, got %d: %s", invalidManifest.status, invalidManifest.body)
	}
	if invalidGet := env.basicRequest(t, http.MethodGet, "/v2/conformance/upload/manifests/sha256:baddigest", nil, "admin", testAdminPassword, nil); invalidGet.status != http.StatusNotFound {
		t.Fatalf("invalid digest pull should return 404, got %d", invalidGet.status)
	}

	viewer, err := env.store.UserByUsername(t.Context(), "viewer")
	if err != nil {
		t.Fatal(err)
	}
	full, tokenID, prefix, secretHash, err := auth.GeneratePAT()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateAPIToken(t.Context(), tokenID, viewer.ID, "test", prefix, secretHash, []string{"pull"}, nil); err != nil {
		t.Fatal(err)
	}
	if patBearer := env.request(t, http.MethodGet, "/v2/", nil, bearer(full)); patBearer.status != http.StatusOK {
		t.Fatalf("Bearer PAT should authenticate /v2/: status=%d body=%s", patBearer.status, patBearer.body)
	}
	if patBasic := env.basicRequest(t, http.MethodGet, "/v2/", nil, "viewer", full, nil); patBasic.status != http.StatusOK {
		t.Fatalf("PAT as Basic password should authenticate: status=%d body=%s", patBasic.status, patBasic.body)
	}
	if wrongUser := env.basicRequest(t, http.MethodGet, "/v2/", nil, "admin", full, nil); wrongUser.status != http.StatusUnauthorized {
		t.Fatalf("PAT must not authenticate as another user, got %d", wrongUser.status)
	}
	if _, exists := env.store.APITokenByID(t.Context(), tokenID); exists != nil {
		t.Fatalf("expected stored PAT: %v", exists)
	}
}
