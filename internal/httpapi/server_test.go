package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"image-todo/internal/assets"
	"image-todo/internal/store"
)

func TestFrontendServesCorrectContentTypes(t *testing.T) {
	frontend := fstest.MapFS{
		"dist/index.html":     {Data: []byte("<html>Done and Drawn</html>")},
		"dist/assets/main.js": {Data: []byte("console.log('ready')")},
	}
	api := New(nil, nil, nil, false, frontend)
	cases := []struct {
		path        string
		contentType string
		body        string
	}{
		{path: "/", contentType: "text/html; charset=utf-8", body: "<html>Done and Drawn</html>"},
		{path: "/tasks", contentType: "text/html; charset=utf-8", body: "<html>Done and Drawn</html>"},
		{path: "/assets/main.js", contentType: "text/javascript; charset=utf-8", body: "console.log('ready')"},
	}
	for _, testCase := range cases {
		t.Run(testCase.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			api.serveFrontend(response, httptest.NewRequest(http.MethodGet, testCase.path, nil))
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != testCase.contentType || response.Body.String() != testCase.body {
				t.Fatalf("frontend response for %s: status=%d type=%q body=%q", testCase.path, response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
		})
	}
}

func TestReferenceUploadLimits(t *testing.T) {
	ctx := context.Background()
	image := validPNG(t)
	for _, testCase := range []struct {
		name       string
		setup      func(*testing.T, *store.Store, *assets.Local)
		image      []byte
		wantStatus int
	}{
		{
			name: "17th image",
			setup: func(t *testing.T, db *store.Store, images *assets.Local) {
				for i := range store.MaxReferenceImages {
					id := fmt.Sprintf("ref-%d", i)
					if err := images.Save("references/"+id+".png", validPNG(t)); err != nil {
						t.Fatal(err)
					}
					if _, err := db.AddReference(ctx, id, "references/"+id+".png"); err != nil {
						t.Fatal(err)
					}
				}
			},
			image:      image,
			wantStatus: http.StatusConflict,
		},
		{
			name: "combined size",
			setup: func(t *testing.T, db *store.Store, images *assets.Local) {
				path := filepath.Join(images.Root(), "references", "large.png")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(store.MaxReferenceBytes - 1); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := db.AddReference(ctx, "large", "references/large.png"); err != nil {
					t.Fatal(err)
				}
			},
			image:      image,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "tasks.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			images, err := assets.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			testCase.setup(t, db, images)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("image", "reference.png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(testCase.image); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/settings/references", &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			request.Header.Set("Origin", "http://example.com")
			response := httptest.NewRecorder()
			New(db, images, nil, false, fstest.MapFS{}, "example.com").Handler().ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("upload status = %d, want %d; body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
		})
	}
}

func TestReferenceUploadValidatesCompleteImage(t *testing.T) {
	valid := validPNG(t)
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name       string
		data       []byte
		wantStatus int
	}{
		{name: "valid PNG", data: valid, wantStatus: http.StatusCreated},
		{name: "valid JPEG", data: jpegData.Bytes(), wantStatus: http.StatusCreated},
		{name: "truncated PNG", data: valid[:len(valid)/2], wantStatus: http.StatusBadRequest},
		{name: "truncated JPEG", data: jpegData.Bytes()[:len(jpegData.Bytes())/2], wantStatus: http.StatusBadRequest},
		{name: "truncated WebP", data: []byte("RIFF\x08\x00\x00\x00WEBP"), wantStatus: http.StatusBadRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "tasks.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			images, err := assets.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			response := uploadReference(t, New(db, images, nil, false, fstest.MapFS{}, "example.com").Handler(), testCase.data, "http://example.com")
			if response.Code != testCase.wantStatus {
				t.Fatalf("upload status = %d, want %d; body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			settings, err := db.GetSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			wantReferences := 0
			if testCase.wantStatus == http.StatusCreated {
				wantReferences = 1
			}
			if len(settings.References) != wantReferences {
				t.Fatalf("saved %d references, want %d", len(settings.References), wantReferences)
			}
		})
	}
}

func TestMissingReferenceCanBeReplaced(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	images, err := assets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddReference(context.Background(), "missing-ref", "references/missing.png"); err != nil {
		t.Fatal(err)
	}

	handler := New(db, images, nil, false, fstest.MapFS{}, "example.com").Handler()
	response := uploadReference(t, handler, validPNG(t), "http://example.com")
	if response.Code != http.StatusCreated {
		t.Fatalf("replacement status = %d, want 201; body=%s", response.Code, response.Body.String())
	}
	settings, err := db.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.References) != 1 || settings.References[0].ID != "missing-ref" || !settings.References[0].IsCanonical || settings.References[0].ImagePath == "references/missing.png" {
		t.Fatalf("missing reference was not replaced in place: %+v", settings.References)
	}
	file, err := images.Open(settings.References[0].ImagePath)
	if err != nil {
		t.Fatalf("replacement image was not stored: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceUploadLimitIsAppliedBeforeReadingBody(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	images, err := assets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := New(db, images, nil, false, fstest.MapFS{}, "example.com")
	api.referenceSlots <- struct{}{}
	body := &trackingReadCloser{Reader: bytes.NewReader([]byte("upload body"))}
	request := httptest.NewRequest(http.MethodPost, "/api/settings/references", nil)
	request.Body = body
	request.Header.Set("Content-Type", "multipart/form-data; boundary=unused")
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent upload status = %d, want 429", response.Code)
	}
	if body.reads != 0 {
		t.Fatalf("rejected request body was read %d times before acquiring an upload slot", body.reads)
	}
}

func TestParseAllowedHosts(t *testing.T) {
	hosts, err := ParseAllowedHosts(" done-and-drawn.local, 192.168.1.12, [fd00::12],done-and-drawn.local ")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 3 || hosts[0] != "done-and-drawn.local" || hosts[1] != "192.168.1.12" || hosts[2] != "fd00::12" {
		t.Fatalf("unexpected normalized allowed hosts: %#v", hosts)
	}
	if _, err := ParseAllowedHosts("0.0.0.0"); err == nil {
		t.Fatal("wildcard host should not be accepted as an allowed Host")
	}
}

func TestWriteOriginCheck(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		method     string
		origin     string
		fetchSite  string
		host       string
		wantStatus int
	}{
		{name: "same origin write", method: http.MethodPost, origin: "http://example.com", fetchSite: "same-origin", wantStatus: http.StatusCreated},
		{name: "cross origin write", method: http.MethodPost, origin: "http://elsewhere.example", fetchSite: "cross-site", wantStatus: http.StatusForbidden},
		{name: "different port", method: http.MethodPost, origin: "http://example.com:5173", wantStatus: http.StatusForbidden},
		{name: "different scheme", method: http.MethodPost, origin: "https://example.com", wantStatus: http.StatusForbidden},
		{name: "null origin write", method: http.MethodPost, origin: "null", wantStatus: http.StatusForbidden},
		{name: "fetch metadata blocks missing origin", method: http.MethodPost, fetchSite: "cross-site", wantStatus: http.StatusForbidden},
		{name: "missing origin", method: http.MethodPost, wantStatus: http.StatusForbidden},
		{name: "cross origin read", method: http.MethodGet, origin: "http://elsewhere.example", fetchSite: "cross-site", wantStatus: http.StatusOK},
		{name: "rebinding write host", method: http.MethodPost, origin: "http://attacker.example", host: "attacker.example", wantStatus: http.StatusForbidden},
		{name: "rebinding read host", method: http.MethodGet, origin: "http://attacker.example", host: "attacker.example", wantStatus: http.StatusForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "tasks.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			images, err := assets.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			body := bytes.NewBufferString(`{"title":"Test task"}`)
			request := httptest.NewRequest(testCase.method, "/api/tasks", body)
			if testCase.host != "" {
				request.Host = testCase.host
			}
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			if testCase.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", testCase.fetchSite)
			}
			response := httptest.NewRecorder()
			New(db, images, nil, false, fstest.MapFS{}, "example.com").Handler().ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("response status = %d, want %d; body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			if testCase.wantStatus == http.StatusForbidden {
				tasks, err := db.ListTasks(context.Background())
				if err != nil || len(tasks) != 0 {
					t.Fatalf("blocked write changed tasks: count=%d err=%v", len(tasks), err)
				}
			}
		})
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	images, err := assets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	response := uploadReference(t, New(db, images, nil, false, fstest.MapFS{}, "example.com").Handler(), validPNG(t), "http://elsewhere.example")
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin multipart upload status = %d, want 403", response.Code)
	}
	settings, err := db.GetSettings(context.Background())
	if err != nil || len(settings.References) != 0 {
		t.Fatalf("cross-origin multipart upload saved a reference: count=%d err=%v", len(settings.References), err)
	}
}

func TestSaveCharacterPromptSections(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	images, err := assets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"appearance":"look","clothing":"coat","home":"cabin","companion":"bird","personality":"bold","art_style":"watercolor"}`
	request := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewBufferString(body))
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	New(db, images, nil, false, fstest.MapFS{}, "example.com").Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("save settings status = %d; body=%s", response.Code, response.Body.String())
	}
	settings, err := db.GetSettings(context.Background())
	if err != nil || settings.Appearance != "look" || settings.Clothing != "coat" || settings.Home != "cabin" || settings.Companion != "bird" || settings.Personality != "bold" || settings.ArtStyle != "watercolor" {
		t.Fatalf("settings sections were not saved: settings=%+v err=%v", settings, err)
	}
}

func validPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

type trackingReadCloser struct {
	*bytes.Reader
	reads int
}

func (body *trackingReadCloser) Read(data []byte) (int, error) {
	body.reads++
	return body.Reader.Read(data)
}

func (body *trackingReadCloser) Close() error { return nil }

func uploadReference(t *testing.T, handler http.Handler, data []byte, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "reference.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/settings/references", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", origin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
