package main

import (
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

func runMigrations() error {
	// Latest migration
	var latestMigration string = ""
	db.QueryRow("SELECT id FROM migrations ORDER BY id DESC LIMIT 1;").Scan(&latestMigration)

	// Initial (2024-02-15)
	// There is technically a previous database schema, but it was too messy to migrate from.
	// The migration for uploads.meower.org was done manually.
	if latestMigration < "2024-02-15" {
		for _, query := range []string{
			// Create tables
			`CREATE TABLE IF NOT EXISTS migrations (
				id CHAR(10) PRIMARY KEY NOT NULL
			);`,
			`CREATE TABLE IF NOT EXISTS icons (
				id CHAR(24) PRIMARY KEY NOT NULL,
				hash CHAR(64) NOT NULL,
				mime VARCHAR(255) NOT NULL,
				size BIGINT NOT NULL,
				width INTEGER NOT NULL,
				height INTEGER NOT NULL,
				uploader VARCHAR(255) NOT NULL,
				uploaded_at BIGINT NOT NULL,
				used_by VARCHAR(255) DEFAULT ''
			);`,
			`CREATE TABLE IF NOT EXISTS attachments (
				id CHAR(24) PRIMARY KEY NOT NULL,
				hash CHAR(64) NOT NULL,
				mime VARCHAR(255) NOT NULL,
				filename VARCHAR(255) NOT NULL,
				size BIGINT NOT NULL,
				width INTEGER NOT NULL,
				height INTEGER NOT NULL,
				uploader VARCHAR(255) NOT NULL,
				uploaded_at BIGINT NOT NULL,
				used_by VARCHAR(255) DEFAULT ''
			);`,
			`CREATE TABLE IF NOT EXISTS blocked (
				hash CHAR(64) PRIMARY KEY NOT NULL,
				auto_ban BOOL NOT NULL /* only for really bad files, auto-bans the uploader if detected */
			);`,

			// Create indexes
			`CREATE INDEX IF NOT EXISTS icons_hash ON icons (hash);`,
			`CREATE INDEX IF NOT EXISTS icons_uploader ON icons (uploader);`,
			`CREATE INDEX IF NOT EXISTS attachments_hash ON attachments (hash);`,
			`CREATE INDEX IF NOT EXISTS unused_attachments ON attachments (
				used_by,
				uploaded_at
			) WHERE used_by = '';`,
			`CREATE INDEX IF NOT EXISTS attachments_uploader ON attachments (uploader);`,

			// Add migrations entry
			`INSERT INTO migrations VALUES ('2024-02-15');`,
		} {
			if _, err := db.Exec(query); err != nil {
				return err
			}
		}
	}

	// Getting ready for custom pfps & chat icons (2024-03-05)
	if latestMigration < "2024-03-05" {
		for _, query := range []string{
			// Drop unused_icons index on icons
			`DROP INDEX IF EXISTS unused_icons;`,

			// Drop used_by column from icons
			`ALTER TABLE icons
			DROP COLUMN used_by;`,

			// Swap width and height columns in attachments
			`UPDATE attachments SET width=height, height=width;`,

			// Add migrations entry
			`INSERT INTO migrations VALUES ('2024-03-05');`,
		} {
			if _, err := db.Exec(query); err != nil {
				return err
			}
		}
	}

	// Clean up filenames
	if latestMigration < "2024-03-31" {
		rows, err := db.Query("SELECT id, filename FROM attachments")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, filename string
			if err := rows.Scan(&id, &filename); err != nil {
				return err
			}
			if _, err := db.Exec("UPDATE attachments SET filename=$1 WHERE id=$2", cleanFilename(filename), id); err != nil {
				return err
			}
		}
		if _, err := db.Exec("INSERT INTO migrations VALUES ('2024-03-31')"); err != nil {
			return err
		}
	}

	// Big DB change
	if latestMigration < "2024-05-12" {
		// Create buckets
		for _, bucketName := range []string{
			"icons",
			"attachments",
			"attachment-previews",
			"data-exports",
		} {
			if exists, _ := s3Clients[s3RegionOrder[0]].BucketExists(ctx, bucketName); !exists {
				if err := s3Clients[s3RegionOrder[0]].MakeBucket(ctx, bucketName, minio.MakeBucketOptions{}); err != nil {
					return err
				}
			}
		}

		// Set lifecycle policy for attachment-previews and data-exports
		config := lifecycle.NewConfiguration()
		config.Rules = []lifecycle.Rule{
			{
				ID:     "expire-after-7-days",
				Status: "Enabled",
				Expiration: lifecycle.Expiration{
					Days: 7,
				},
			},
		}
		s3Clients[s3RegionOrder[0]].SetBucketLifecycle(ctx, "attachment-previews", config)
		s3Clients[s3RegionOrder[0]].SetBucketLifecycle(ctx, "data-exports", config)

		// Create files table
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS files (
			id CHAR(24) PRIMARY KEY NOT NULL,
			hash CHAR(64) NOT NULL,
			bucket VARCHAR(255) NOT NULL,
			filename VARCHAR(255) NOT NULL DEFAULT '',
			width INTEGER NOT NULL DEFAULT 0,
			height INTEGER NOT NULL DEFAULT 0,
			uploaded_by VARCHAR(255) NOT NULL,
			uploaded_at BIGINT NOT NULL,
			claimed BOOLEAN NOT NULL DEFAULT false
		);`); err != nil {
			return err
		}

		// Migrate icons
		rows, err := db.Query(`SELECT
			id,
			hash,
			width,
			height,
			uploader,
			uploaded_at
		FROM icons;`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f File
			if err := rows.Scan(
				&f.Id,
				&f.Hash,
				&f.Width,
				&f.Height,
				&f.UploadedBy,
				&f.UploadedAt,
			); err != nil {
				return err
			}

			if _, err := db.Exec(`INSERT INTO files (
				id,
				hash,
				bucket,
				width,
				height,
				uploaded_by,
				uploaded_at,
				claimed
			) VALUES (
				$1,
				$2,
				'icons',
				$3,
				$4,
				$5,
				$6,
				true
			);`, f.Id, f.Hash, f.Width, f.Height, f.UploadedBy, f.UploadedAt); err != nil {
				return err
			}
		}

		// Migrate attachments
		rows, err = db.Query(`SELECT
			id,
			hash,
			filename,
			width,
			height,
			uploader,
			uploaded_at
		FROM attachments;`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f File
			if err := rows.Scan(
				&f.Id,
				&f.Hash,
				&f.Filename,
				&f.Width,
				&f.Height,
				&f.UploadedBy,
				&f.UploadedAt,
			); err != nil {
				return err
			}

			if _, err := db.Exec(`INSERT INTO files (
				id,
				hash,
				bucket,
				filename,
				width,
				height,
				uploaded_by,
				uploaded_at,
				claimed
			) VALUES (
				$1,
				$2,
				'attachments',
				$3,
				$4,
				$5,
				$6,
				$7,
				true
			);`, f.Id, f.Hash, f.Filename, f.Width, f.Height, f.UploadedBy, f.UploadedAt); err != nil {
				return err
			}
		}

		// Create indexes, drop icons/attachments tables, and add migration entry
		for _, query := range []string{
			// Create indexes
			`CREATE INDEX IF NOT EXISTS files_hash ON files (hash);`,
			`CREATE INDEX IF NOT EXISTS files_uploader ON files (uploaded_by);`,
			`CREATE INDEX IF NOT EXISTS unclaimed_files ON files (
				claimed,
				uploaded_at
			) WHERE claimed = false;`,

			// Drop icons and attachments tables
			`DROP TABLE icons;`,
			`DROP TABLE attachments;`,

			// Add migrations entry
			`INSERT INTO migrations VALUES ('2024-05-12');`,
		} {
			if _, err := db.Exec(query); err != nil {
				return err
			}
		}
	}

	// Cross-region support (2024-06-15)
	if latestMigration < "2024-06-15" {
		// Add mime column
		query := `ALTER TABLE files
		ADD mime VARCHAR(255);`
		if _, err := db.Exec(query); err != nil {
			return err
		}

		// Add upload_region column
		query = `ALTER TABLE files
		ADD upload_region VARCHAR(255);`
		if _, err := db.Exec(query); err != nil {
			return err
		}

		// Set upload_region
		query = `UPDATE files SET upload_region=$1;`
		if _, err := db.Exec(query, s3RegionOrder[0]); err != nil {
			return err
		}

		// Set mime
		query = "UPDATE files SET mime=$1 WHERE hash=$2 AND bucket=$3;"
		for _, bucket := range []string{
			"icons",
			"attachments",
		} {
			for object := range s3Clients[s3RegionOrder[0]].ListObjects(ctx, bucket, minio.ListObjectsOptions{}) {
				if _, err := db.Exec(query, object.ContentType, object.Key, bucket); err != nil {
					return err
				}
			}
		}

		// Add migrations entry
		query = "INSERT INTO migrations VALUES ('2024-06-15');"
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}

	return nil
}
