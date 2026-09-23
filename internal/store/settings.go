package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var settings Settings
	var updatedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT character_prompt, style_prompt, updated_at FROM character_settings WHERE id = 1`).Scan(&settings.CharacterPrompt, &settings.StylePrompt, &updatedAt); err != nil {
		return Settings{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Settings{}, err
	}
	settings.UpdatedAt = parsed
	rows, err := s.db.QueryContext(ctx, `SELECT id, image_path, is_canonical, created_at FROM character_reference_images ORDER BY is_canonical DESC, created_at ASC`)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	settings.References = make([]ReferenceImage, 0)
	for rows.Next() {
		var reference ReferenceImage
		var canonical int
		var created string
		if err := rows.Scan(&reference.ID, &reference.ImagePath, &canonical, &created); err != nil {
			return Settings{}, err
		}
		reference.IsCanonical = canonical == 1
		reference.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return Settings{}, err
		}
		settings.References = append(settings.References, reference)
	}
	if err := rows.Err(); err != nil {
		return Settings{}, err
	}
	settings.CanGenerate = len(settings.References) > 0
	return settings, nil
}

func (s *Store) SaveSettings(ctx context.Context, characterPrompt, stylePrompt string) (Settings, error) {
	_, err := s.db.ExecContext(ctx, `UPDATE character_settings SET character_prompt = ?, style_prompt = ?, updated_at = ? WHERE id = 1`, characterPrompt, stylePrompt, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Settings{}, err
	}
	return s.GetSettings(ctx)
}

func (s *Store) AddReference(ctx context.Context, id, imagePath string) (ReferenceImage, error) {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReferenceImage{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM character_reference_images`).Scan(&count); err != nil {
		return ReferenceImage{}, err
	}
	canonical := 0
	if count == 0 {
		canonical = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO character_reference_images (id, image_path, is_canonical, created_at) VALUES (?, ?, ?, ?)`, id, imagePath, canonical, now.Format(time.RFC3339Nano)); err != nil {
		return ReferenceImage{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReferenceImage{}, err
	}
	return ReferenceImage{ID: id, ImagePath: imagePath, IsCanonical: canonical == 1, CreatedAt: now}, nil
}

func (s *Store) SetCanonicalReference(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE character_reference_images SET is_canonical = 0`); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE character_reference_images SET is_canonical = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) DeleteReference(ctx context.Context, id string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var path string
	var canonical int
	if err := tx.QueryRowContext(ctx, `SELECT image_path, is_canonical FROM character_reference_images WHERE id = ?`, id).Scan(&path, &canonical); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM character_reference_images`).Scan(&count); err != nil {
		return "", err
	}
	if count <= 1 {
		return "", ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM character_reference_images WHERE id = ?`, id); err != nil {
		return "", err
	}
	if canonical == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE character_reference_images SET is_canonical = 1 WHERE id = (SELECT id FROM character_reference_images ORDER BY created_at, id LIMIT 1)`); err != nil {
			return "", fmt.Errorf("select replacement canonical image: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return path, nil
}
