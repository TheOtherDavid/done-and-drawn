package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/webp"

	"image-todo/internal/assets"
	"image-todo/internal/store"
)

const maxJSONBytes = 1 << 20
const maxImageBytes = 15 << 20
const maxUploadBodyBytes = maxImageBytes + (1 << 20)
const maxImagePixels = 20_000_000
const maxPromptFieldBytes = 12_000
const maxPromptTotalBytes = 24_000

type API struct {
	store          *store.Store
	assets         *assets.Local
	wake           chan<- struct{}
	apiKeyPresent  bool
	frontend       fs.FS
	referenceMu    sync.Mutex
	referenceSlots chan struct{}
	allowedHosts   map[string]struct{}
}

func New(database *store.Store, imageStore *assets.Local, wake chan<- struct{}, apiKeyPresent bool, frontend fs.FS, configuredHosts ...string) *API {
	dist, err := fs.Sub(frontend, "dist")
	if err != nil {
		dist = frontend
	}
	allowedHosts := localAllowedHosts()
	for _, configured := range configuredHosts {
		if host, ok := normalizeHost(configured); ok {
			allowedHosts[host] = struct{}{}
		}
	}
	return &API{
		store: database, assets: imageStore, wake: wake, apiKeyPresent: apiKeyPresent, frontend: dist,
		referenceSlots: make(chan struct{}, 1), allowedHosts: allowedHosts,
	}
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
	return secureHeaders(checkAllowedHost(a.allowedHosts, checkWriteOrigin(mux)))
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
		Appearance  string `json:"appearance"`
		Clothing    string `json:"clothing"`
		Home        string `json:"home"`
		Companion   string `json:"companion"`
		Personality string `json:"personality"`
		ArtStyle    string `json:"art_style"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	prompts := []string{input.Appearance, input.Clothing, input.Home, input.Companion, input.Personality, input.ArtStyle}
	totalPromptBytes := 0
	for _, prompt := range prompts {
		if len(prompt) > maxPromptFieldBytes {
			writeError(w, http.StatusBadRequest, "Use at most 12,000 characters in each field and 24,000 total")
			return
		}
		totalPromptBytes += len(prompt)
	}
	if totalPromptBytes > maxPromptTotalBytes {
		writeError(w, http.StatusBadRequest, "Use at most 12,000 characters in each field and 24,000 total")
		return
	}
	settings, err := a.store.SaveSettings(r.Context(), input.Appearance, input.Clothing, input.Home, input.Companion, input.Personality, input.ArtStyle)
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
	select {
	case a.referenceSlots <- struct{}{}:
		defer func() { <-a.referenceSlots }()
	default:
		writeError(w, http.StatusTooManyRequests, "Another reference image is being processed. Try again shortly.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBodyBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
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
	if err := validateReferenceImage(data, contentType); err != nil {
		writeError(w, http.StatusBadRequest, "Image is incomplete, invalid, or too large to decode")
		return
	}
	a.referenceMu.Lock()
	defer a.referenceMu.Unlock()
	settings, err := a.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not check reference images")
		return
	}
	totalBytes, missingReference, err := a.referenceInfo(settings.References)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not check reference image sizes")
		return
	}
	if totalBytes+int64(len(data)) > store.MaxReferenceBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "Reference images must stay under 32 MiB combined")
		return
	}
	if missingReference == nil && len(settings.References) >= store.MaxReferenceImages {
		writeError(w, http.StatusConflict, "Keep at most 16 reference images")
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
	var reference store.ReferenceImage
	if missingReference != nil {
		reference, err = a.store.ReplaceReferenceImage(r.Context(), missingReference.ID, imagePath)
	} else {
		reference, err = a.store.AddReference(r.Context(), id, imagePath)
	}
	if err != nil {
		_ = a.assets.Delete(imagePath)
		if errors.Is(err, store.ErrReferenceLimit) {
			writeError(w, http.StatusConflict, "Keep at most 16 reference images")
			return
		}
		writeError(w, http.StatusInternalServerError, "Could not save reference image")
		return
	}
	reference.ImageURL = mediaURL(reference.ImagePath)
	writeJSON(w, http.StatusCreated, reference)
}

func (a *API) referenceInfo(references []store.ReferenceImage) (int64, *store.ReferenceImage, error) {
	var total int64
	var missing *store.ReferenceImage
	for _, reference := range references {
		file, err := a.assets.Open(reference.ImagePath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if missing == nil {
					copy := reference
					missing = &copy
				}
				continue
			}
			return 0, nil, err
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil {
			return 0, nil, statErr
		}
		if closeErr != nil {
			return 0, nil, closeErr
		}
		total += info.Size()
	}
	return total, missing, nil
}

func (a *API) setCanonicalReference(w http.ResponseWriter, r *http.Request) {
	if err := a.store.SetCanonicalReference(r.Context(), r.PathValue("id")); err != nil {
		a.writeStoreError(w, err, "Could not select canonical image")
		return
	}
	a.getSettings(w, r)
}

func (a *API) deleteReference(w http.ResponseWriter, r *http.Request) {
	a.referenceMu.Lock()
	defer a.referenceMu.Unlock()
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

func validateReferenceImage(data []byte, contentType string) error {
	var config image.Config
	var err error
	switch contentType {
	case "image/png":
		config, err = png.DecodeConfig(bytes.NewReader(data))
	case "image/jpeg":
		config, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case "image/webp":
		config, err = webp.DecodeConfig(bytes.NewReader(data))
	default:
		return errors.New("unsupported image type")
	}
	if err != nil {
		return err
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxImagePixels {
		return errors.New("image dimensions exceed limit")
	}
	switch contentType {
	case "image/png":
		_, err = png.Decode(bytes.NewReader(data))
	case "image/jpeg":
		_, err = jpeg.Decode(bytes.NewReader(data))
	case "image/webp":
		_, err = webp.Decode(bytes.NewReader(data))
	}
	return err
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

func checkAllowedHost(allowed map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, ok := requestHostname(r.Host)
		if !ok {
			writeError(w, http.StatusForbidden, "This host is not configured for local access")
			return
		}
		if _, ok := allowed[host]; !ok {
			writeError(w, http.StatusForbidden, "This host is not configured for local access")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localAllowedHosts() map[string]struct{} {
	allowed := map[string]struct{}{"localhost": {}, "127.0.0.1": {}, "::1": {}}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return allowed
	}
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			continue
		}
		ip := prefix.Addr().WithZone("")
		if !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			continue
		}
		allowed[ip.String()] = struct{}{}
	}
	return allowed
}

func requestHostname(hostport string) (string, bool) {
	var host string
	if strings.HasPrefix(hostport, "[") {
		if parsedHost, port, err := net.SplitHostPort(hostport); err == nil {
			if !validHostPort(port) {
				return "", false
			}
			host = parsedHost
		} else if strings.HasSuffix(hostport, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
		} else {
			return "", false
		}
	} else if strings.Count(hostport, ":") == 1 {
		parsedHost, port, err := net.SplitHostPort(hostport)
		if err != nil || !validHostPort(port) {
			return "", false
		}
		host = parsedHost
	} else if strings.Contains(hostport, ":") {
		return "", false
	} else {
		host = hostport
	}
	return normalizeHost(host)
}

func validHostPort(port string) bool {
	value, err := strconv.ParseUint(port, 10, 16)
	return err == nil && value > 0
}

func normalizeHost(value string) (string, bool) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), true
	}
	if len(host) == 0 || len(host) > 253 || strings.ContainsAny(host, "/\\?#@: ") {
		return "", false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", false
			}
		}
	}
	return host, true
}

func ParseAllowedHosts(config string) ([]string, error) {
	var hosts []string
	seen := make(map[string]struct{})
	for _, entry := range strings.Split(config, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		host, ok := normalizeHost(entry)
		if !ok {
			return nil, errors.New("APP_ALLOWED_HOSTS must be a comma-separated list of hostnames or IP addresses")
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			return nil, errors.New("APP_ALLOWED_HOSTS cannot contain wildcard IP addresses")
		}
		if _, exists := seen[host]; !exists {
			hosts = append(hosts, host)
			seen[host] = struct{}{}
		}
	}
	return hosts, nil
}

func checkWriteOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			origin := r.Header.Get("Origin")
			parsed, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if origin == "" || err != nil || parsed.Scheme != scheme || !strings.EqualFold(parsed.Host, r.Host) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				writeError(w, http.StatusForbidden, "A same-origin request is required for writes")
				return
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				writeError(w, http.StatusForbidden, "A same-origin request is required for writes")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
