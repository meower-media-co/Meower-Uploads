package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

type VolumeServerLocation struct {
	Url string `json:"url"`
}

type DirAssignment struct {
	VolumeServerLocation
	FileID string `json:"fid"`
}

type DirLookup struct {
	Locations []VolumeServerLocation `json:"locations"`
}

type TokenClaims struct {
	Type      string            `msgpack:"t"` // can be 'upload_file' or 'view_file' for uploads server
	ExpiresAt int64             `msgpack:"e"`
	Upload    TokenUploadClaims `msgpack:"d"`
}

type TokenUploadClaims struct {
	ID         string `msgpack:"id"`
	MaxSize    int64  `msgpack:"max_size"`
	Compressed bool   `msgpack:"allow_uncompressed"`
}

func getFileHash(data []byte) string {
	hash := sha256.Sum256(data)
	hashHex := hex.EncodeToString(hash[:])
	return hashHex
}

func saveFile(data []byte, filename string) (string, error) {
	// Get SeaweedFS master server
	var seaweedFSMaster = ""
	if seaweedFSMaster = os.Getenv("SEAWEEDFS_MASTER"); seaweedFSMaster == "" {
		seaweedFSMaster = "http://127.0.0.1:9333"
	}

	// Assign a File ID within SeaweedFS master server
	resp, err := http.Get(seaweedFSMaster + "/dir/assign")
	if err != nil {
		log.Fatalln(err)
	}
	defer resp.Body.Close()
	var dirAssignment DirAssignment
	err = json.NewDecoder(resp.Body).Decode(&dirAssignment)
	if err != nil {
		log.Fatalln(err)
	}

	// Upload the file to SeaweedFS volume server
	req, err := http.NewRequest("PUT", "http://"+dirAssignment.Url+"/"+dirAssignment.FileID, bytes.NewReader(data))
	if err != nil {
		return dirAssignment.FileID, err
	}
	req.Header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	client := &http.Client{}
	resp, err = client.Do(req)
	if err != nil {
		return dirAssignment.FileID, err
	}
	defer resp.Body.Close()

	return dirAssignment.FileID, nil
}

func loadFile(fid string) (*http.Response, error) {
	// Get SeaweedFS master server
	var seaweedFSMaster = ""
	if seaweedFSMaster = os.Getenv("SEAWEEDFS_MASTER"); seaweedFSMaster == "" {
		seaweedFSMaster = "http://127.0.0.1:9333"
	}

	// Get where the file's volume server is
	resp, err := http.Get(seaweedFSMaster + "/dir/lookup?volumeId=" + fid)
	if err != nil {
		log.Fatalln(err)
	}
	defer resp.Body.Close()
	var dirLookup DirLookup
	err = json.NewDecoder(resp.Body).Decode(&dirLookup)
	if err != nil {
		log.Fatalln(err)
	}

	// Return file from volume server
	uri := "http://" + dirLookup.Locations[0].Url + "/" + fid
	return http.Get(uri)
}

func getTokenClaims(tokenString string) (bool, *TokenClaims, error) {
	// Split token string
	splitArgs := strings.Split(tokenString, ".")
	if len(splitArgs) != 2 {
		return false, nil, fmt.Errorf("failed to split token string")
	}
	encodedClaims := splitArgs[0]
	encodedSignature := splitArgs[1]

	// Decode claims
	decodedClaims, err := base64.StdEncoding.DecodeString(encodedClaims)
	if err != nil {
		fmt.Println(err)
		return false, nil, fmt.Errorf("failed to decode token claims")
	}

	// Parse claims
	var claims TokenClaims
	err = msgpack.Unmarshal(decodedClaims, &claims)
	if err != nil {
		return false, nil, fmt.Errorf("failed to parse token claims")
	}

	// Make sure token hasn't expired
	if claims.ExpiresAt <= time.Now().Unix() {
		return false, nil, fmt.Errorf("token has expired")
	}

	// Decode signature
	decodedSignature, err := base64.StdEncoding.DecodeString(encodedSignature)
	if err != nil {
		return false, &claims, fmt.Errorf("failed to decode token signature")
	}

	// Validate signature
	hmacHasher := hmac.New(sha256.New, []byte("abc"))
	hmacHasher.Write(decodedClaims)
	if !reflect.DeepEqual(decodedSignature, hmacHasher.Sum(nil)) {
		return false, &claims, fmt.Errorf("invalid token signature")
	}

	return true, &claims, nil
}
