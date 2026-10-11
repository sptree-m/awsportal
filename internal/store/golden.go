package store

import (
	"context"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const goldenSchema = `CREATE TABLE IF NOT EXISTS golden_images(profile_id TEXT NOT NULL,version TEXT NOT NULL,ami_id TEXT NOT NULL,launch_template_id TEXT NOT NULL,launch_template_version TEXT NOT NULL,checksum TEXT NOT NULL,validation_ref TEXT NOT NULL,channel TEXT NOT NULL CHECK(channel IN ('STABLE','NEXT','ARCHIVED')),created_at INTEGER NOT NULL,PRIMARY KEY(profile_id,version));CREATE UNIQUE INDEX IF NOT EXISTS golden_channel ON golden_images(profile_id,channel) WHERE channel IN ('STABLE','NEXT');`

type GoldenImage struct{ ProfileID, Version, AMI, TemplateID, TemplateVersion, Checksum, ValidationRef, Channel string }

func (s *Store) RegisterGolden(ctx context.Context, u User, g GoldenImage) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	if g.ProfileID != "shared-cpu-v1" && g.ProfileID != "windows-box-v1" && g.ProfileID != "personal-v1" {
		return fmt.Errorf("approved profile required")
	}
	if !regexp.MustCompile(`^ami-[a-f0-9]{8,17}$`).MatchString(g.AMI) || !regexp.MustCompile(`^lt-[a-f0-9]{8,17}$`).MatchString(g.TemplateID) || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(g.TemplateVersion) || g.Version == "" || len(g.Checksum) != 64 || g.ValidationRef == "" || g.Channel != "NEXT" {
		return fmt.Errorf("pinned AMI/template, checksum and validation evidence required; register NEXT first")
	}
	if _, err := hex.DecodeString(g.Checksum); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO golden_images VALUES(?,?,?,?,?,?,?,?,?)`, g.ProfileID, g.Version, g.AMI, g.TemplateID, g.TemplateVersion, strings.ToLower(g.Checksum), g.ValidationRef, g.Channel, time.Now().Unix())
	return err
}
func (s *Store) PromoteGolden(ctx context.Context, u User, profile, version, reason string) error {
	if err := adminOnly(u); err != nil {
		return err
	}
	if reason == "" {
		return fmt.Errorf("promotion reason required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM golden_images WHERE profile_id=? AND version=? AND channel='NEXT')`, profile, version).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("validated NEXT image required")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE golden_images SET channel='ARCHIVED' WHERE profile_id=? AND channel='STABLE'`, profile); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE golden_images SET channel='STABLE' WHERE profile_id=? AND version=?`, profile, version); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_events(kind,detail,at) VALUES('golden_promoted',?,?)`, u.Username+" "+profile+" "+version+": "+reason, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) GoldenImages(ctx context.Context) ([]GoldenImage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT profile_id,version,ami_id,launch_template_id,launch_template_version,checksum,validation_ref,channel FROM golden_images ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GoldenImage
	for rows.Next() {
		var x GoldenImage
		if err = rows.Scan(&x.ProfileID, &x.Version, &x.AMI, &x.TemplateID, &x.TemplateVersion, &x.Checksum, &x.ValidationRef, &x.Channel); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
