package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"image-todo/internal/assets"
	"image-todo/internal/store"
)

const maxJSONBytes = 1 << 20
const maxImageBytes = 15 << 20
const maxUploadBodyBytes = maxImageBytes + (1 << 20)

type API struct {
	store         *store.Store
	assets        *assets.Local
	wake          chan<- struct{}
	apiKeyPresent bool
	frontend      fs.FS
}

func New(database *store.Store, imageStore *assets.Local, wake chan<- struct{}, apiKeyPresent bool, frontend fs.FS) *API {
	dist, err := fs.Sub(frontend, "dist")
	if err != nil {
		dist = frontend
	}
	return &API{store: database, assets: imageStore, wake: wake, apiKeyPresent: apiKeyPresent, frontend: dist}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/tasks", a.listTasks)
	mux.HandleFunc("POST /api/tasks", a.createTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", a.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", a.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/complete", a.completeTask)
	mux.HandleFunc("POST /api/tasks/{id}/retry", a.retryTask)
	mux.HandleFunc("GET /api/settings", a.getSettings)
	mux.HandleFunc("PUT /api/settings", a.saveSettings)
	mux.HandleFunc("POST /api/settings/references", a.addReference)
	mux.HandleFunc("PUT /api/settings/references/{id}/canonical", a.setCanonicalReference)
	mux.HandleFunc("DELETE /api/settings/references/{id}", a.deleteReference)
	mux.Handle("GET /media/", http.StripPrefix("/media/", http.FileServer(http.Dir(a.assets.Root()))))
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "API route not found")
	})
	mux.HandleFunc("GET /", a.serveFrontend)
	return secureHeaders(mux)
}

func (a *API) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := a.store.ListTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load tasks")
		return
	}
	for i := range tasks {
		tasks[i] = publicTask(tasks[i])
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (a *API) createTask(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Title) == "" {
		writeError(w, http.StatusBadRequest, "Task title is required")
		return
	}
	if len(input.Title) > 300 || len(input.Description) > 5000 {
		writeError(w, http.StatusBadRequest, "Task text is too long")
		return
	}
	task, err := a.store.CreateTask(r.Context(), input.Title, input.Description)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not create task")
		return
	}
	writeJSON(w, http.StatusCreated, publicTask(task))
}

func (a *API) updateTask(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Title) == "" || len(input.Title) > 300 || len(input.Description) > 5000 {
		writeError(w, http.StatusBadRequest, "Enter a task title under 300 characters")
		return
	}
	task, err := a.store.UpdateTask(r.Context(), r.PathValue("id"), input.Title, input.Description)
	if err != nil {
		a.writeStoreError(w, err, "Completed tasks cannot be edited")
		return
	}
	writeJSON(w, http.StatusOK, publicTask(task))
}

func (a *API) deleteTask(w http.ResponseWriter, r *http.Request) {
	imagePath, err := a.store.DeleteTask(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeStoreError(w, err, "Completed tasks cannot be deleted")
		return
	}
	if imagePath != "" {
		_ = a.assets.Delete(imagePath)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) completeTask(w http.ResponseWriter, r *http.Request) {
	task, queued, err := a.store.CompleteTask(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeStoreError(w, err, "Could not complete task")
		return
	}
	if queued {
		select {
		case a.wake <- struct{}{}:
		default:
		}
	}
	writeJSON(w, http.StatusOK, publicTask(task))
}

func (a *API) retryTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.store.RetryTask(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeStoreError(w, err, "Only failed rewards can be retried")
		return
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusAccepted, publicTask(task))
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load character settings")
		return
	}
	settings.CanGenerate = a.apiKeyPresent && hasCanonical(settings.References)
	for i := range settings.References {
		settings.References[i].ImageURL = mediaURL(settings.References[i].ImagePath)
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *API) saveSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CharacterPrompt string `json:"character_prompt"`
		StylePrompt     string `json:"style_prompt"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.CharacterPrompt) > 12000 || len(input.StylePrompt) > 12000 {
		writeError(w, http.StatusBadRequest, "Prompts must be under 12,000 characters")
		return
	}
	settings, err := a.store.SaveSettings(r.Context(), input.CharacterPrompt, input.StylePrompt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save character settings")
		return
	}
	settings.CanGenerate = a.apiKeyPresent && hasCanonical(settings.References)
	for i := range settings.References {
		settings.References[i].ImageURL = mediaURL(settings.References[i].ImagePath)
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *API) addReference(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBodyBytes)
	if err := r.ParseMultipartForm(maxUploadBodyBytes); err != nil {
		writeError(w, http.StatusBadRequest, "Choose an image smaller than 15 MB")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Choose an image file")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxImageBytes {
		writeError(w, http.StatusBadRequest, "Could not read image; maximum size is 15 MB")
		return
	}
	contentType := detectImageType(data)
	extensions := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}
	extension, ok := extensions[contentType]
	if !ok {
		writeError(w, http.StatusBadRequest, "Use a PNG, JPEG, or WebP image")
		return
	}
	id, err := store.NewID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save image")
		return
	}
	imagePath := "references/" + id + extension
	if err := a.assets.Save(imagePath, data); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save reference image")
		return
	}
	reference, err := a.store.AddReference(r.Context(), id, imagePath)
	if err != nil {
		_ = a.assets.Delete(imagePath)
		writeError(w, http.StatusInternalServerError, "Could not save reference image")
		return
	}
	reference.ImageURL = mediaURL(reference.ImagePath)
	writeJSON(w, http.StatusCreated, reference)
}

func (a *API) setCanonicalReference(w http.ResponseWriter, r *http.Request) {
	if err := a.store.SetCanonicalReference(r.Context(), r.PathValue("id")); err != nil {
		a.writeStoreError(w, err, "Could not select canonical image")
		return
	}
	a.getSettings(w, r)
}

func (a *API) deleteReference(w http.ResponseWriter, r *http.Request) {
	imagePath, err := a.store.DeleteReference(r.Context(), r.PathValue("id"))
	if err != nil {
		a.writeStoreError(w, err, "Keep at least one reference image")
		return
	}
	if err := a.assets.Delete(imagePath); err != nil {
		writeError(w, http.StatusInternalServerError, "Reference removed, but its image file could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) writeStoreError(w http.ResponseWriter, err error, conflictMessage string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Item not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, conflictMessage)
	default:
		writeError(w, http.StatusInternalServerError, "The request could not be completed")
	}
}

func (a *API) serveFrontend(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." || strings.HasPrefix(name, "api/") || strings.HasPrefix(name, "media/") {
		name = "index.html"
	}
	data, err := fs.ReadFile(a.frontend, name)
	if err != nil {
		name = "index.html"
		data, err = fs.ReadFile(a.frontend, "index.html")
	}
	if err != nil {
		http.Error(w, "Frontend is not built. Run npm run build from frontend/.", http.StatusServiceUnavailable)
		return
	}
	contentType := ""
	switch filepath.Ext(name) {
	case ".html":
		contentType = "text/html; charset=utf-8"
	case ".css":
		contentType = "text/css; charset=utf-8"
	case ".js":
		contentType = "text/javascript; charset=utf-8"
	default:
		contentType = mime.TypeByExtension(filepath.Ext(name))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "Request body must contain one JSON object")
		return false
	}
	return true
}

func publicTask(task store.Task) store.Task {
	if task.RewardImagePath != "" {
		task.RewardImageURL = mediaURL(task.RewardImagePath)
	}
	return task
}

func mediaURL(relativePath string) string {
	parts := strings.Split(filepath.ToSlash(relativePath), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/media/" + strings.Join(parts, "/")
}

func hasCanonical(references []store.ReferenceImage) bool {
	for _, reference := range references {
		if reference.IsCanonical {
			return true
		}
	}
	return false
}

func detectImageType(data []byte) string {
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return http.DetectContentType(data[:min(len(data), 512)])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
