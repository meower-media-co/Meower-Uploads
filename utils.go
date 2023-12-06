package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/vmihailenco/msgpack/v5"
)

type TokenClaims struct {
	Type      string `msgpack:"t"` // can be 'upload_icon' or 'upload_attachment'
	ExpiresAt int64  `msgpack:"e"`
	Data      struct {
		UploadID string `msgpack:"id"`
		UserID   string `msgpack:"u"`
		MaxSize  int64  `msgpack:"s"`
	} `msgpack:"d"`
}

func createMinIOBuckets() error {
	if exists, _ := minioClient.BucketExists(ctx, "icons"); !exists {
		err := minioClient.MakeBucket(ctx, "icons", minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalln(err)
		}
	}
	if exists, _ := minioClient.BucketExists(ctx, "attachments"); !exists {
		err := minioClient.MakeBucket(ctx, "attachments", minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalln(err)
		}
	}
	if exists, _ := minioClient.BucketExists(ctx, "user-exports"); !exists {
		err := minioClient.MakeBucket(ctx, "user-exports", minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalln(err)
		}
	}
	if exists, _ := minioClient.BucketExists(ctx, "db-backups"); !exists {
		err := minioClient.MakeBucket(ctx, "db-backups", minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalln(err)
		}
	}

	return nil
}

func createDBTables() error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS icons (
			id STRING PRIMARY KEY,
			hash STRING,
			mime STRING,
			size BIGINT,
			width INT,
			height INT,
			uploaded_by STRING,
			uploaded_at BIGINT,
			used_by STRING
		);
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS icons_hash ON icons (hash);
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS unused_icons ON icons (
			used_by,
			uploaded_at
		) WHERE used_by = '';
	`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS attachments (
			id STRING PRIMARY KEY,
			hash STRING,
			mime STRING,
			filename STRING,
			size BIGINT,
			width INT,
			height INT,
			uploaded_by STRING,
			uploaded_at BIGINT,
			used_by STRING
		);
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS attachments_hash ON attachments (hash);
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS unused_attachments ON attachments (
			used_by,
			uploaded_at
		) WHERE used_by = '';
	`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS user_exports (
			id STRING PRIMARY KEY,
			user_id STRING,
			size BIGINT,
			created_at BIGINT
		);
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS old_user_exports ON user_exports (created_at);
	`); err != nil {
		return err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS blocked (
			hash STRING PRIMARY KEY,
			reason STRING,
			auto_ban BOOL, /* only for really bad files, auto-bans the uploader if detected */
			blocked_by STRING,
			blocked_at BIGINT
		);
	`); err != nil {
		return err
	}

	return nil
}

func getTokenClaims(tokenString string) (*TokenClaims, error) {
	// Split token string
	splitArgs := strings.Split(tokenString, ".")
	if len(splitArgs) != 2 {
		return nil, fmt.Errorf("failed to split token string")
	}
	encodedClaims := splitArgs[0]
	encodedSignature := splitArgs[1]

	// Decode claims
	decodedClaims, err := base64.URLEncoding.DecodeString(encodedClaims)
	if err != nil {
		fmt.Println(err)
		return nil, fmt.Errorf("failed to decode token claims")
	}

	// Parse claims
	var claims TokenClaims
	err = msgpack.Unmarshal(decodedClaims, &claims)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token claims")
	}

	// Make sure token hasn't expired
	if claims.ExpiresAt == time.Now().Unix() {
		return nil, fmt.Errorf("token has expired")
	}

	// Decode signature
	decodedSignature, err := base64.URLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return &claims, fmt.Errorf("failed to decode token signature")
	}

	// Validate signature
	hmacHasher := hmac.New(sha256.New, []byte(os.Getenv("TOKEN_SECRET")))
	hmacHasher.Write(decodedClaims)
	if !reflect.DeepEqual(decodedSignature, hmacHasher.Sum(nil)) {
		return &claims, fmt.Errorf("invalid token signature")
	}

	return &claims, nil
}

// Delete unused icons that are more than 10 minutes old
func cleanupIcons() {
	rows, err := db.Query("SELECT id, hash FROM icons WHERE used_by = '' AND uploaded_at < $1", time.Now().Unix()-600)
	if err != nil {
		log.Println(err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var err error
		var id, hash string
		var multipleExists bool

		if err = rows.Scan(&id, &hash); err != nil {
			log.Println(err)
			continue
		}

		err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM icons WHERE hash = $1 AND id != $2)", hash, id).Scan(&multipleExists)
		if err != nil {
			log.Println(err)
			continue
		}

		if !multipleExists {
			minioClient.RemoveObject(ctx, "icons", hash, minio.RemoveObjectOptions{})
		}

		_, err = db.Exec("DELETE FROM icons WHERE id=$1", id)
		if err != nil {
			log.Println(err)
			continue
		}
	}

	if err := rows.Err(); err != nil {
		log.Println(err)
		return
	}
}

// Delete unused attachments that are more than 10 minutes old
func cleanupAttachments() {
	rows, err := db.Query("SELECT id, hash FROM attachments WHERE used_by = '' AND uploaded_at < $1", time.Now().Unix()-600)
	if err != nil {
		log.Println(err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var err error
		var id, hash string
		var multipleExists bool

		if err = rows.Scan(&id, &hash); err != nil {
			log.Println(err)
			continue
		}

		err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM attachments WHERE hash = $1 AND id != $2)", hash, id).Scan(&multipleExists)
		if err != nil {
			log.Println(err)
			continue
		}

		if !multipleExists {
			minioClient.RemoveObject(ctx, "attachments", hash, minio.RemoveObjectOptions{})
		}

		_, err = db.Exec("DELETE FROM attachments WHERE id=$1", id)
		if err != nil {
			log.Println(err)
			continue
		}
	}

	if err := rows.Err(); err != nil {
		log.Println(err)
		return
	}
}

// Delete user exports that are more than 7 days old
func cleanupUserExports() {
	rows, err := db.Query("SELECT id FROM user_exports WHERE created_at < $1", time.Now().Unix()-259200)
	if err != nil {
		log.Println(err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var err error
		var id string

		if err = rows.Scan(&id); err != nil {
			log.Println(err)
			continue
		}

		err = minioClient.RemoveObject(ctx, "user_exports", id, minio.RemoveObjectOptions{})
		if err != nil {
			log.Println(err)
			continue
		}

		_, err = db.Exec("DELETE FROM user_exports WHERE id=$1", id)
		if err != nil {
			log.Println(err)
			continue
		}
	}

	if err := rows.Err(); err != nil {
		log.Println(err)
		return
	}
}

// Get the block status of a file by its hash.
// Returns whether it's blocked and whether to auto-ban the uploader.
func getBlockStatus(hashHex string) (bool, bool, error) {
	var autoBan bool
	err := db.QueryRow("SELECT auto_ban FROM blocked WHERE hash=$1", hashHex).Scan(&autoBan)
	if err == sql.ErrNoRows {
		return false, false, nil
	} else if err != nil {
		return false, false, err
	} else {
		return true, autoBan, nil
	}
}

// Contact the main API to ban a user by their ID.
func banUser(userID string) error {
	log.Println(userID, "would've been banned")
	return nil
}
