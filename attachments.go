package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/discord/lilliput"
	"github.com/go-chi/chi/v5"
	"github.com/minio/minio-go/v7"
)

type Attachment struct {
	Id         string `json:"id"`
	Hash       string `json:"hash"`
	Mime       string `json:"mime"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Uploader   string `json:"uploader"`
	UploadedAt int64  `json:"uploaded_at"`
	UsedBy     string `json:"used_by"`
}

func attachmentsRouter(r chi.Router) {
	r.Get("/{id}/{filename}", func(w http.ResponseWriter, r *http.Request) {
		// Get attachment details from database
		var attachment Attachment
		err := db.QueryRow(
			"SELECT * FROM attachments WHERE id=$1 AND filename=$2",
			chi.URLParam(r, "id"),
			cleanFilename(chi.URLParam(r, "filename")),
		).Scan(
			&attachment.Id,
			&attachment.Hash,
			&attachment.Mime,
			&attachment.Filename,
			&attachment.Size,
			&attachment.Width,
			&attachment.Height,
			&attachment.Uploader,
			&attachment.UploadedAt,
			&attachment.UsedBy,
		)
		if err == sql.ErrNoRows {
			http.Error(w, "sowwy uwu i c-couldnt find t-that f-file :3", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "oopsies i did a fucky wucky uppy and i broeke something :3.. pls fix it urself", http.StatusInternalServerError)
			return
		}

		// ETag caching
		if r.Header.Get("ETag") == attachment.Hash || r.Header.Get("If-None-Match") == attachment.Hash {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		// Get object from MinIO
		object, err := s3.GetObject(ctx, "attachments", attachment.Hash, minio.GetObjectOptions{})
		if err != nil {
			http.Error(w, "ur mon lmaooo", http.StatusInternalServerError)
			return
		}
		defer object.Close()

		// Set response headers
		w.Header().Set("Content-Type", attachment.Mime)
		w.Header().Set("Content-Length", strconv.FormatInt(attachment.Size, 10))
		// Make sure browsers download a file rather than embedding if it's big (over 8MiB)
		if attachment.Size > (8 << 20) {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%s`, attachment.Filename))
		}
		w.Header().Set("ETag", attachment.Hash)
		w.Header().Set("Cache-Control", "pbulic, max-age=31536000") // 1 year cache (files should never change)

		// Copy the object data into the response body
		_, err = io.Copy(w, object)
		if err != nil {
			http.Error(w, "delicious", http.StatusInternalServerError)
			return
		}
	})

	r.Post("/", func(w http.ResponseWriter, r *http.Request) {
		// Get token claims
		tokenClaims, err := getTokenClaims(r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, "tnix got angwy at u haha", http.StatusUnauthorized)
			return
		}

		// Make sure token is valid
		if tokenClaims.Type != "upload_attachment" {
			http.Error(w, "tnix got ewen mowe angy at  u hhahahha", http.StatusUnauthorized)
			return
		}

		// Get file from request body
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "u fucked up u loser", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Make sure file doesn't exceeed maximum size
		if header.Size > tokenClaims.Data.MaxSize {
			http.Error(w, "ur thing is to big for me", http.StatusRequestEntityTooLarge)
			return
		}

		// Get file hash
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		if err != nil {
			http.Error(w, "i fucked up lol ur problems now :3", http.StatusInternalServerError)
			return
		}

		// Get hash hex
		hashHex := hex.EncodeToString(hash.Sum(nil))

		// Make sure file hash isn't blocked
		blocked, autoBan, err := getBlockStatus(hashHex)
		if err != nil {
			http.Error(w, "die", http.StatusInternalServerError)
			return
		} else if blocked {
			if autoBan {
				go banUser(tokenClaims.Data.Uploader, hashHex)
			}
			http.Error(w, "womp womp cry about it", http.StatusForbidden)
			return
		}

		// Get attachment details (if one exists with the same hash)
		var attachment Attachment
		db.QueryRow("SELECT * FROM attachments WHERE hash=$1", hashHex).Scan(
			&attachment.Id,
			&attachment.Hash,
			&attachment.Mime,
			&attachment.Filename,
			&attachment.Size,
			&attachment.Width,
			&attachment.Height,
			&attachment.Uploader,
			&attachment.UploadedAt,
			&attachment.UsedBy,
		)

		var width int
		var height int
		if attachment.Hash != hashHex {
			// Upload to MinIO
			file.Seek(0, 0)
			_, err = s3.PutObject(ctx, "attachments", hashHex, file, header.Size, minio.PutObjectOptions{
				ContentType: header.Header.Get("Content-Type"),
			})
			if err != nil {
				http.Error(w, "Failed to save attachment", http.StatusInternalServerError)
				return
			}

			// Get width and height if the file is a supported media file
			fileExt := map[string]string{
				"image/png":  ".png",
				"image/jpeg": ".jpeg",
				"image/webp": ".webp",
				"image/gif":  ".gif",
				"video/mov":  ".mov",
				"video/webm": ".webm",
			}[header.Header.Get("Content-Type")]
			if fileExt != "" && strings.HasSuffix(header.Filename, fileExt) {
				func() {
					fileBytes, err := io.ReadAll(file)
					if err != nil {
						return
					}

					decoder, err := lilliput.NewDecoder(fileBytes)
					if err != nil {
						return
					}
					defer decoder.Close()

					lilliputHeader, err := decoder.Header()
					if err != nil {
						return
					}

					width = lilliputHeader.Width()
					height = lilliputHeader.Height()
				}()
			}
		}

		// Create new attachment details
		attachment = Attachment{
			Id:         tokenClaims.Data.UploadId,
			Hash:       hashHex,
			Mime:       header.Header.Get("Content-Type"),
			Filename:   cleanFilename(header.Filename),
			Size:       header.Size,
			Width:      width,
			Height:     height,
			Uploader:   tokenClaims.Data.Uploader,
			UploadedAt: time.Now().Unix(),
		}

		// Save attachment details to database
		_, err = db.Exec(`INSERT INTO attachments VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);`,
			attachment.Id,
			attachment.Hash,
			attachment.Mime,
			attachment.Filename,
			attachment.Size,
			attachment.Width,
			attachment.Height,
			attachment.Uploader,
			attachment.UploadedAt,
			attachment.UsedBy,
		)
		if err != nil {
			http.Error(w, "Failed to save attachment details", http.StatusInternalServerError)
			return
		}

		// Return attachment details
		encoded, err := json.Marshal(attachment)
		if err != nil {
			http.Error(w, "Failed to send attachment details", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
	})
}
