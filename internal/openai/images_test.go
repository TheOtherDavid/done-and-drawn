package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"image-todo/internal/assets"
	"image-todo/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSelectReferencesCapsCountAndKeepsCanonical(t *testing.T) {
	images, err := assets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := New("test-key", "test-model").WithAssets(images)
	references := make([]store.ReferenceImage, 0, store.MaxReferenceImages+1)
	for i := range store.MaxReferenceImages + 1 {
		path := fmt.Sprintf("references/%02d.png", i)
		if err := images.Save(path, []byte("x")); err != nil {
			t.Fatal(err)
		}
		references = append(references, store.ReferenceImage{ImagePath: path, IsCanonical: i == store.MaxReferenceImages})
	}
	selected, err := client.selectReferences(references)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != store.MaxReferenceImages || !selected[0].IsCanonical {
		t.Fatalf("expected canonical plus 15 references, got %d: %+v", len(selected), selected)
	}
	imageParts := 0
	canonicalSent := false
	client.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		reader, err := request.MultipartReader()
		if err != nil {
			return nil, err
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if part.FormName() == "image[]" {
				imageParts++
				canonicalSent = canonicalSent || part.FileName() == filepath.Base(references[store.MaxReferenceImages].ImagePath)
			}
			if _, err := io.Copy(io.Discard, part); err != nil {
				return nil, err
			}
		}
		body := fmt.Sprintf(`{"data":[{"b64_json":%q}]}`, base64.StdEncoding.EncodeToString([]byte("generated")))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
	})}
	if _, err := client.Generate(context.Background(), store.Task{Title: "Finish task"}, store.Settings{References: references}); err != nil {
		t.Fatal(err)
	}
	if imageParts != store.MaxReferenceImages || !canonicalSent {
		t.Fatalf("request sent %d images; canonical sent=%v", imageParts, canonicalSent)
	}
}

func TestSelectReferencesCapsCombinedSize(t *testing.T) {
	images, err := assets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := New("test-key", "test-model").WithAssets(images)
	if err := os.MkdirAll(filepath.Join(images.Root(), "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	references := []store.ReferenceImage{
		{ImagePath: "references/canonical.png", IsCanonical: true},
		{ImagePath: "references/second.png"},
		{ImagePath: "references/third.png"},
	}
	for i, size := range []int64{15 << 20, 15 << 20, 3 << 20} {
		file, err := os.Create(filepath.Join(images.Root(), filepath.FromSlash(references[i].ImagePath)))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(size); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := client.selectReferences(references)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].ImagePath != references[0].ImagePath || selected[1].ImagePath != references[1].ImagePath {
		t.Fatalf("expected references within 32 MiB, got %+v", selected)
	}
}

func TestGenerateSendsReferenceImageMIMEType(t *testing.T) {
	cases := []struct {
		name        string
		extension   string
		contentType string
	}{
		{name: "PNG", extension: ".png", contentType: "image/png"},
		{name: "JPEG", extension: ".jpg", contentType: "image/jpeg"},
		{name: "WebP", extension: ".webp", contentType: "image/webp"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			images, err := assets.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			imagePath := "references/character" + testCase.extension
			referenceBytes := []byte("reference image")
			if err := images.Save(imagePath, referenceBytes); err != nil {
				t.Fatal(err)
			}

			generatedBytes := []byte("generated image")
			encoded := base64.StdEncoding.EncodeToString(generatedBytes)
			imageParts := 0
			fields := map[string]string{}
			client := New("test-key", "test-model").WithAssets(images)
			client.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodPost || request.URL.Path != "/v1/images/edits" {
					t.Errorf("unexpected image request: %s %s", request.Method, request.URL)
				}
				if request.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("request is missing backend authorization")
				}
				reader, err := request.MultipartReader()
				if err != nil {
					return nil, err
				}
				for {
					part, err := reader.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						return nil, err
					}
					content, err := io.ReadAll(part)
					if err != nil {
						return nil, err
					}
					if part.FormName() == "image[]" {
						imageParts++
						if got := part.Header.Get("Content-Type"); got != testCase.contentType {
							t.Errorf("reference MIME type = %q, want %q", got, testCase.contentType)
						}
						if part.FileName() != filepath.Base(imagePath) || !bytes.Equal(content, referenceBytes) {
							t.Errorf("reference attachment changed: filename=%q bytes=%q", part.FileName(), content)
						}
					} else {
						fields[part.FormName()] = string(content)
					}
				}
				body := fmt.Sprintf(`{"data":[{"b64_json":%q}]}`, encoded)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
					Request:    request,
				}, nil
			})}

			settings := store.Settings{
				Appearance:  "red scarf",
				Clothing:    "jacket",
				Home:        "cottage",
				Companion:   "small fox",
				Personality: "curious",
				ArtStyle:    "ink drawing",
				References:  []store.ReferenceImage{{ImagePath: imagePath, IsCanonical: true}},
			}
			result, err := client.Generate(context.Background(), store.Task{Title: "Water plants", Description: "Back patio"}, settings)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result, generatedBytes) || imageParts != 1 {
				t.Fatalf("unexpected generated response or image count: bytes=%q parts=%d", result, imageParts)
			}
			if fields["model"] != "test-model" || fields["size"] != "1024x1024" || fields["quality"] != "medium" {
				t.Errorf("image request fields are incomplete: %+v", fields)
			}
			for _, text := range []string{"Appearance:\nred scarf", "Clothing:\njacket", "Home:\ncottage", "Companion:\nsmall fox", "Personality:\ncurious", "Art style:\nink drawing", "Water plants", "Back patio"} {
				if !strings.Contains(fields["prompt"], text) {
					t.Errorf("prompt is missing %q", text)
				}
			}
		})
	}
}
