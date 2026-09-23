package store

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type RewardStatus string

const (
	RewardNone       RewardStatus = "none"
	RewardQueued     RewardStatus = "queued"
	RewardGenerating RewardStatus = "generating"
	RewardReady      RewardStatus = "ready"
	RewardFailed     RewardStatus = "failed"
)

type Task struct {
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Description     string       `json:"description"`
	CreatedAt       time.Time    `json:"created_at"`
	CompletedAt     *time.Time   `json:"completed_at,omitempty"`
	RewardStatus    RewardStatus `json:"reward_status"`
	RewardImagePath string       `json:"-"`
	RewardImageURL  string       `json:"reward_image_url,omitempty"`
	RewardError     string       `json:"reward_error,omitempty"`
}

type ReferenceImage struct {
	ID          string    `json:"id"`
	ImagePath   string    `json:"-"`
	ImageURL    string    `json:"image_url"`
	IsCanonical bool      `json:"is_canonical"`
	CreatedAt   time.Time `json:"created_at"`
}

type Settings struct {
	CharacterPrompt string           `json:"character_prompt"`
	StylePrompt     string           `json:"style_prompt"`
	UpdatedAt       time.Time        `json:"updated_at"`
	References      []ReferenceImage `json:"references"`
	CanGenerate     bool             `json:"can_generate"`
}

func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-4" + encoded[13:16] + "-a" + encoded[17:20] + "-" + encoded[20:], nil
}
