package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	Id         string `json:"id"`
	Hash       string `json:"hash"`
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Uploader   string `json:"uploaded_by"`
	UploadedAt int64  `json:"uploaded_at"`
}

func iconsRouter(r chi.Router) {
	r.Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Get icon ID and extension
		iconId := chi.URLParam(r, "id")
		ext := ""
		if strings.Contains(iconId, ".") {
			// Split
			parts := strings.Split(iconId, ".")
			if len(parts) != 2 {
				http.Error(w, "Icon not found", http.StatusNotFound)
				return
			}
			iconId = parts[0]
			ext = strings.ToLower(parts[1])

			// Convert extension & make sure it's supported
			if ext == "webp" || ext == "gif" {
				ext = ""
			} else if ext == "jpg" {
				ext = "jpeg"
			}
			if ext != "" && ext != "png" && ext != "jpeg" {
				http.Error(w, "Unsupported conversion", http.StatusBadRequest)
				return
			}
		}

		// Get icon details from database
		var icon Icon
		err := db.QueryRow("SELECT * FROM icons WHERE id=$1", iconId).Scan(
			&icon.Id,
			&icon.Hash,
			&icon.Mime,
			&icon.Size,
			&icon.Width,
			&icon.Height,
			&icon.Uploader,
			&icon.UploadedAt,
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
		object, err := s3.GetObject(ctx, "icons", icon.Hash, minio.GetObjectOptions{})
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

		if ext == "" {
			// Copy the object data into the response body
			_, err = io.Copy(w, object)
			if err != nil {
				http.Error(w, "Failed to send icon object", http.StatusInternalServerError)
				return
			}
		} else {
			// Get file bytes
			fileBytes := make([]byte, (8 << 20))
			_, err = object.Read(fileBytes)
			if err != io.EOF {
				log.Println(err)
				http.Error(w, "Failed to read file", http.StatusInternalServerError)
				return
			}

			// Get lilliput decoder
			lilliputDecoder, err := lilliput.NewDecoder(fileBytes)
			if err != nil {
				log.Println(err)
				http.Error(w, "Failed to resize/convert image", http.StatusInternalServerError)
				return
			}
			defer lilliputDecoder.Close()

			// Create options
			options := lilliput.ImageOptions{
				FileType:     fmt.Sprint(".", ext),
				ResizeMethod: lilliput.ImageOpsResize,
				Width:        icon.Width,
				Height:       icon.Height,
			}

			// Create ops
			ops := lilliput.NewImageOps(8192)
			defer ops.Close()

			// Resize & convert image
			fileBytes, err = ops.Transform(lilliputDecoder, &options, fileBytes)
			if err != nil {
				log.Println(err)
				http.Error(w, "Failed to resize/convert image", http.StatusInternalServerError)
				return
			}

			// Set new mime and size
			w.Header().Set("Content-Type", map[string]string{
				".png":  "image/png",
				".jpeg": "image/jpeg",
			}[ext])
			w.Header().Set("Content-Length", strconv.FormatInt(int64(len(fileBytes)), 10))

			// Write file bytes
			w.Write(fileBytes)
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

		// Read file
		fileBytes := make([]byte, (8 << 20))
		_, err = file.Read(fileBytes)
		if err != nil {
			http.Error(w, "Failed to read file", http.StatusInternalServerError)
			return
		}

		// Get file hash
		hash := sha256.New()
		_, err = hash.Write(fileBytes)
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
				go banUser(tokenClaims.Data.Uploader, hashHex)
			}
			http.Error(w, "File is blocked", http.StatusForbidden)
			return
		}

		// Initialise icon details
		icon := Icon{
			Id:         tokenClaims.Data.UploadId,
			Hash:       hashHex,
			Uploader:   tokenClaims.Data.Uploader,
			UploadedAt: time.Now().Unix(),
		}

		// Get icon details (if one exists with the same hash)
		db.QueryRow(`SELECT (
			mime,
			size,
			width,
			height
		) FROM icons WHERE hash=$1`, hashHex).Scan(
			&icon.Mime,
			&icon.Size,
			&icon.Width,
			&icon.Height,
		)

		if icon.Mime == "" {
			// Get file extension
			fileExt := map[string]string{
				"image/png":  ".webp",
				"image/jpeg": ".webp",
				"image/webp": ".webp",
				"image/gif":  ".gif",
			}[header.Header.Get("Content-Type")]
			if fileExt == "" {
				http.Error(w, "Unsupported mime type", http.StatusBadRequest)
				return
			}
			if fileExt == ".gif" {
				icon.Mime = "image/gif"
			} else {
				icon.Mime = "image/webp"
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
			icon.Width = lilliputHeader.Width()
			if icon.Width > 256 {
				icon.Width = 256
			}
			icon.Height = lilliputHeader.Height()
			if icon.Height > 256 {
				icon.Height = 256
			}

			// Create options
			options := lilliput.ImageOptions{
				FileType:     fileExt,
				ResizeMethod: lilliput.ImageOpsFit,
				Width:        icon.Width,
				Height:       icon.Height,
			}

			// Create ops
			ops := lilliput.NewImageOps(8192)
			defer ops.Close()

			// Resize & convert image
			fileBytes, err = ops.Transform(lilliputDecoder, &options, fileBytes)
			if err != nil {
				log.Println(err)
				http.Error(w, "Failed to resize image", http.StatusInternalServerError)
				return
			}
			icon.Size = int64(len(fileBytes))

			// Upload to MinIO
			_, err = s3.PutObject(ctx, "icons", hashHex, bytes.NewReader(fileBytes), int64(len(fileBytes)), minio.PutObjectOptions{
				ContentType: icon.Mime,
			})
			if err != nil {
				log.Println(err)
				http.Error(w, "Failed to save icon", http.StatusInternalServerError)
				return
			}
		}

		// Save icon details to database
		_, err = db.Exec(`INSERT INTO icons VALUES ($1, $2, $3, $4, $5, $6, $7, $8);`,
			icon.Id,
			icon.Hash,
			icon.Mime,
			icon.Size,
			icon.Width,
			icon.Height,
			icon.Uploader,
			icon.UploadedAt,
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
