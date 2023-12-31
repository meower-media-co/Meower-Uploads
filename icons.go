package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/discord/lilliput"
	"github.com/go-chi/chi/v5"
	"github.com/minio/minio-go/v7"
)

type Icon struct {
	ID         string `json:"id"`
	Hash       string `json:"hash"`
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	UploadedBy string `json:"uploaded_by"`
	UploadedAt int64  `json:"uploaded_at"`
	UsedBy     string `json:"used_by"`
}

func IconsRouter(r chi.Router) {
	r.Get("/{id}.{fileExt}", func(w http.ResponseWriter, r *http.Request) {
		// Get icon details from database
		var icon Icon
		err := db.QueryRow("SELECT * FROM icons WHERE id=$1", chi.URLParam(r, "id")).Scan(
			&icon.ID,
			&icon.Hash,
			&icon.Mime,
			&icon.Size,
			&icon.Width,
			&icon.Height,
			&icon.UploadedBy,
			&icon.UploadedAt,
			&icon.UsedBy,
		)
		if err == sql.ErrNoRows {
			http.Error(w, "Icon not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "Failed to get icon details", http.StatusInternalServerError)
			return
		}

		// ETag caching
		if r.Header.Get("If-None-Match") == icon.Hash {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		// Get object from MinIO
		object, err := minioClient.GetObject(ctx, "icons", icon.Hash, minio.GetObjectOptions{})
		if err != nil {
			http.Error(w, "Failed to get icon object", http.StatusInternalServerError)
			return
		}
		defer object.Close()

		// Set response headers
		w.Header().Set("Content-Type", icon.Mime)
		w.Header().Set("Content-Length", strconv.FormatInt(icon.Size, 10))
		w.Header().Set("ETag", icon.Hash)
		w.Header().Set("Cache-Control", "pbulic, max-age=31536000") // 1 year cache (files should never change)

		// Copy the object data into the response body
		_, err = io.Copy(w, object)
		if err != nil {
			http.Error(w, "Failed to send icon object", http.StatusInternalServerError)
			return
		}
	})

	r.Post("/", func(w http.ResponseWriter, r *http.Request) {
		// Get token claims
		tokenClaims, err := getTokenClaims(r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Make sure token is valid
		if tokenClaims.Type != "upload_icon" {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Get file from request body
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "Invalid form", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Make sure file doesn't exceeed maximum size
		if header.Size > tokenClaims.Data.MaxSize {
			http.Error(w, "File too large", http.StatusRequestEntityTooLarge)
			return
		}

		// Get file hash
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		if err != nil {
			http.Error(w, "Failed to calculate hash", http.StatusInternalServerError)
			return
		}

		// Get hash hex
		hashHex := hex.EncodeToString(hash.Sum(nil))

		// Make sure file hash isn't blocked
		blocked, autoBan, err := getBlockStatus(hashHex)
		if err != nil {
			http.Error(w, "Failed to check block status", http.StatusInternalServerError)
			return
		} else if blocked {
			if autoBan {
				go banUser(tokenClaims.Data.UserID)
			}
			http.Error(w, "File is blocked", http.StatusForbidden)
			return
		}

		// Get icon details (if one exists with the same hash)
		var icon Icon
		db.QueryRow("SELECT * FROM icons WHERE hash=$1", hashHex).Scan(
			&icon.ID,
			&icon.Hash,
			&icon.Mime,
			&icon.Size,
			&icon.Width,
			&icon.Height,
			&icon.UploadedBy,
			&icon.UploadedAt,
			&icon.UsedBy,
		)

		var width = icon.Width
		var height = icon.Height
		if icon.Hash != hashHex {
			// Get file extension
			fileExt := map[string]string{
				"image/png":  ".webp",
				"image/jpeg": ".webp",
				"image/webp": ".webp",
				"image/gif":  ".gif",
			}[header.Header.Get("Content-Type")]
			if fileExt == "" || !strings.HasSuffix(header.Filename, fileExt) {
				http.Error(w, "Failed to calculate hash", http.StatusBadRequest)
				return
			}

			// Get file bytes
			file.Seek(0, 0)
			fileBytes, err := io.ReadAll(file)
			if err != nil {
				log.Println(err)
				return
			}

			// Get lilliput decoder
			lilliputDecoder, err := lilliput.NewDecoder(fileBytes)
			if err != nil {
				log.Println(err)
				return
			}
			defer lilliputDecoder.Close()

			// Get lilliput header
			lilliputHeader, err := lilliputDecoder.Header()
			if err != nil {
				log.Println(err)
				return
			}

			// Get width and height
			width = lilliputHeader.Width()
			height = lilliputHeader.Height()

			// Create ops
			ops := lilliput.NewImageOps(8192)
			defer ops.Close()

			// Create options
			options := lilliput.ImageOptions{
				ResizeMethod: lilliput.ImageOpsFit,
			}
			if width > 256 {
				options.Width = 256
			}
			if height > 256 {
				options.Height = 256
			}

			// Resize & convert image
			fileBytes, err = ops.Transform(lilliputDecoder, &options, fileBytes)
			if err != nil {
				http.Error(w, "Failed to resize image", http.StatusInternalServerError)
				return
			}

			// Upload to MinIO
			_, err = minioClient.PutObject(ctx, "icons", hashHex, bytes.NewReader(fileBytes), int64(len(fileBytes)), minio.PutObjectOptions{
				ContentType: header.Header.Get("Content-Type"),
			})
			if err != nil {
				log.Println(err)
				http.Error(w, "Failed to save icon", http.StatusInternalServerError)
				return
			}

			// Set new mime and size
			if fileExt != ".gif" {
				header.Header.Set("Content-Type", "image/webp")
			}
			header.Size = int64(len(fileBytes))
		}

		// Create new icon details
		icon = Icon{
			ID:         tokenClaims.Data.UploadID,
			Hash:       hashHex,
			Mime:       header.Header.Get("Content-Type"),
			Size:       header.Size,
			Width:      width,
			Height:     height,
			UploadedBy: tokenClaims.Data.UserID,
			UploadedAt: time.Now().Unix(),
		}

		// Save icon details to database
		_, err = db.Exec(`INSERT INTO icons VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);`,
			icon.ID,
			icon.Hash,
			icon.Mime,
			icon.Size,
			icon.Width,
			icon.Height,
			icon.UploadedBy,
			icon.UploadedAt,
			icon.UsedBy,
		)
		if err != nil {
			http.Error(w, "Failed to save icon details", http.StatusInternalServerError)
			return
		}

		// Return icon details
		encoded, err := json.Marshal(icon)
		if err != nil {
			http.Error(w, "Failed to send icon details", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
	})
}
