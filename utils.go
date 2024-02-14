package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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
	Type      string `msgpack:"t"` // can be 'upload_icon', 'upload_attachment', or 'access_data_export'
	ExpiresAt int64  `msgpack:"e"`
	Data      struct {
		UploadId string `msgpack:"id"`
		Uploader string `msgpack:"u"`
		MaxSize  int64  `msgpack:"s"`
	} `msgpack:"d"`
}

func createMinIOBuckets() error {
	if exists, _ := s3.BucketExists(ctx, "icons"); !exists {
		err := s3.MakeBucket(ctx, "icons", minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalln(err)
		}
	}
	if exists, _ := s3.BucketExists(ctx, "attachments"); !exists {
		err := s3.MakeBucket(ctx, "attachments", minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalln(err)
		}
	}
	if exists, _ := s3.BucketExists(ctx, "data-exports"); !exists {
		err := s3.MakeBucket(ctx, "data-exports", minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalln(err)
		}
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
			s3.RemoveObject(ctx, "icons", hash, minio.RemoveObjectOptions{})
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
			s3.RemoveObject(ctx, "attachments", hash, minio.RemoveObjectOptions{})
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

// Send a request to the main server to ban a user by their username for
// uploading a blocked file.
func banUser(username string, fileHash string) error {
	marshaledEvent, err := json.Marshal(map[string]string{
		"op":     "ban_user",
		"user":   username,
		"state":  "perm_ban",
		"reason": "",
		"note":   "Automatically banned by the uploads server for uploading a file that was blocked and set to auto-ban the uploader.\nFile hash: " + fileHash,
	})
	if err != nil {
		return err
	}
	err = rdb.Publish(ctx, "admin", marshaledEvent).Err()
	return err
}
