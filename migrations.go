package main

func runDBMigrations() error {
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
			`UPDATE TABLE attachments SET width=height, height=width;`,

			// Add migrations entry
			`INSERT INTO migrations VALUES ('2024-03-05');`,
		} {
			if _, err := db.Exec(query); err != nil {
				return err
			}
		}
	}

	return nil
}
