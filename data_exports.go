package main

import (
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/minio/minio-go/v7"
)

func dataExportsRouter(r chi.Router) {
	r.Get("/{token}", func(w http.ResponseWriter, r *http.Request) {
		// Get token claims
		tokenClaims, err := getTokenClaims(chi.URLParam(r, "token"))
		if err != nil {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Make sure token is valid
		if tokenClaims.Type != "access_data_export" {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Get object from MinIO
		object, err := s3.GetObject(ctx, "data-exports", tokenClaims.Data.UploadId, minio.GetObjectOptions{})
		if err != nil {
			http.Error(w, "Failed to get data export object", http.StatusInternalServerError)
			return
		}
		defer object.Close()
		objectStat, err := object.Stat()
		if err != nil {
			http.Error(w, "Failed to get data export object", http.StatusInternalServerError)
			return
		}

		// Set response headers
		w.Header().Set("Content-Type", objectStat.ContentType)
		w.Header().Set("Content-Length", strconv.FormatInt(objectStat.Size, 10))
		w.Header().Set("Content-Disposition", "attachment; filename=meower_export.zip")
		w.Header().Set("Cache-Control", "none")

		// Copy the object data into the response body
		_, err = io.Copy(w, object)
		if err != nil {
			http.Error(w, "Failed to send data export object", http.StatusInternalServerError)
			return
		}
	})
}
