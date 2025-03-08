package main

import (
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

// Complete the (currently empty) handlerUploadVideo handler to store video files in S3. Images will stay on the local file system for now. I recommend using the image upload handler as a reference.
func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// Set an upload limit of 1 GB (1 << 30 bytes) using http.MaxBytesReader.
	const maxMemory = 1 << 30
	http.MaxBytesReader(w, r.Body, maxMemory)

	// Extract the videoID from the URL path parameters and parse it as a UUID
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// Authenticate the user to get a userID
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Get the video metadata from the database, if the user is not the video owner, return a http.StatusUnauthorized response
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User is not the video owner", nil)
		return
	}

	// Parse the uploaded video file from the form data

	// Use (http.Request).FormFile with the key "video" to get a multipart.File in memory
	videoFile, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	// Remember to defer closing the file with (os.File).Close - we don't want any memory leaks
	defer videoFile.Close()

	// Validate the uploaded file to ensure it's an MP4 video
	// - Use mime.ParseMediaType and "video/mp4" as the MIME type
	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse media type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid media type", nil)
		return
	}

	// Save the uploaded file to a temporary file on disk.

	// Use os.CreateTemp to create a temporary file. I passed in an empty string for the directory to use the system default, and the name "tubely-upload.mp4" (but you can use whatever you want)
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create temp file", err)
		return
	}
	// defer remove the temp file with os.Remove
	defer os.Remove(tempFile.Name())
	// defer close the temp file (defer is LIFO, so it will close before the remove)
	defer tempFile.Close()

	// io.Copy the contents over from the wire to the temp file
	_, err = io.Copy(tempFile, videoFile)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save video file", err)
		return
	}

	// Reset the tempFile's file pointer to the beginning with .Seek(0, io.SeekStart) - this will allow us to read the file again from the beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to seek temp file", err)
		return
	}

	// The file key. Use the same <random-32-byte-hex>.ext format as the key. e.g. 1a2b3c4d5e6f7890abcd1234ef567890.mp4
	randomHex, err := uuid.NewRandom()
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to generate random hex", err)
		return
	}
	fileKey := randomHex.String() + ".mp4"

	// Put the object into S3 using PutObject
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &fileKey,
		Body:        tempFile,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to upload video to S3", err)
		return
	}

	// Update the VideoURL of the video record in the database with the S3 bucket and key. S3 URLs are in the format https://<bucket-name>.s3.<region>.amazonaws.com/<key>. Make sure you use the correct region and bucket name!
	videoURL := "https://" + cfg.s3Bucket + ".s3." + cfg.s3Region + ".amazonaws.com/" + fileKey
	video.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video", err)
		return
	}
}
