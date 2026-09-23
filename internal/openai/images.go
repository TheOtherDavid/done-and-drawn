package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
	"time"

	"image-todo/internal/assets"
	"image-todo/internal/store"
)

type Client struct {
	apiKey string
	model  string
	http   *http.Client
	assets *assets.Local
}

func New(apiKey, model string) *Client {
	return &Client{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *Client) WithAssets(imageStore *assets.Local) *Client {
	c.assets = imageStore
	return c
}

func (c *Client) Generate(ctx context.Context, task store.Task, settings store.Settings) ([]byte, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("image generation is not configured: set OPENAI_API_KEY on the backend")
	}
	hasCanonical := false
	for _, reference := range settings.References {
		if reference.IsCanonical {
			hasCanonical = true
			break
		}
	}
	if !hasCanonical {
		return nil, fmt.Errorf("add a canonical reference image in settings before generating rewards")
	}
	if c.assets == nil {
		return nil, fmt.Errorf("image storage is unavailable")
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	prompt := makePrompt(task, settings)
	fields := map[string]string{
		"model":   c.model,
		"prompt":  prompt,
		"size":    "1024x1024",
		"quality": "medium",
	}
	for name, value := range fields {
		if err := multipartWriter.WriteField(name, value); err != nil {
			return nil, err
		}
	}
	for _, reference := range settings.References {
		contentType, err := referenceContentType(reference.ImagePath)
		if err != nil {
			return nil, err
		}
		file, err := c.assets.Open(reference.ImagePath)
		if err != nil {
			return nil, fmt.Errorf("open reference image: %w", err)
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
			"name":     "image[]",
			"filename": filepath.Base(reference.ImagePath),
		}))
		header.Set("Content-Type", contentType)
		part, err := multipartWriter.CreatePart(header)
		if err == nil {
			_, err = io.Copy(part, file)
		}
		closeErr := file.Close()
		if err != nil {
			return nil, fmt.Errorf("attach reference image: %w", err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close reference image: %w", closeErr)
		}
	}
	if err := multipartWriter.Close(); err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/images/edits", &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("image API request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return nil, fmt.Errorf("image API returned %s: %s", response.Status, strings.TrimSpace(string(limited)))
	}
	var result struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 50<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode image API response: %w", err)
	}
	if len(result.Data) == 0 || result.Data[0].B64JSON == "" {
		return nil, fmt.Errorf("image API returned no image data")
	}
	imageBytes, err := base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("decode generated image: %w", err)
	}
	return imageBytes, nil
}

func referenceContentType(imagePath string) (string, error) {
	switch strings.ToLower(filepath.Ext(imagePath)) {
	case ".jpg", ".jpeg":
		return "image/jpeg", nil
	case ".png":
		return "image/png", nil
	case ".webp":
		return "image/webp", nil
	default:
		return "", fmt.Errorf("unsupported reference image type: %s", filepath.Ext(imagePath))
	}
}

func makePrompt(task store.Task, settings store.Settings) string {
	var sections []string
	if prompt := strings.TrimSpace(settings.CharacterPrompt); prompt != "" {
		sections = append(sections, "Character definition:\n"+prompt)
	}
	if prompt := strings.TrimSpace(settings.StylePrompt); prompt != "" {
		sections = append(sections, "Visual style:\n"+prompt)
	}
	sections = append(sections,
		"Create one warm, celebratory reward illustration showing the character from the provided reference images completing or celebrating this task. Preserve the character's recognizable appearance. Do not add text or lettering to the image.",
		"Task: "+task.Title,
	)
	if strings.TrimSpace(task.Description) != "" {
		sections = append(sections, "Task details: "+task.Description)
	}
	return strings.Join(sections, "\n\n")
}
