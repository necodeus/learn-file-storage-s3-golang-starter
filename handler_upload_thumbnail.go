package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// 2. Parse the form data
	// 2.1 Set a const maxMemory to 10MB. I just bit-shifted the number 10 to the left 20 times to get an int that stores the proper number of bytes.
	const maxMemory = 10 << 20

	// 2.2 Use (http.Request).ParseMultipartForm with the maxMemory const as an argument
	r.ParseMultipartForm(maxMemory)

	// 3.1 Use r.FormFile to get the file data. The key the web browser is using is called "thumbnail"
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	// 3.2 Get the media type from the file's Content-Type header
	mediaType := header.Header.Get("Content-Type")

	// 4. Read all the image data into a byte slice using io.ReadAll
	data, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to read file", err)
		return
	}

	// 5. Get the video's metadata from the SQLite database. The apiConfig's db has a GetVideo method you can use
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video", err)
		return
	}
	// If the authenticated user is not the video owner, return a http.StatusUnauthorized response
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User is not the video owner", nil)
		return
	}

	// 6. Save the thumbnail to the global map
	// 6.1 Create a new thumbnail struct with the image data and media type
	// 6.2 Add the thumbnail to the global map, using the video's ID as the key
	videoThumbnails[videoID] = thumbnail{data, mediaType}

	// 7. Update the database so that the existing video record has a new thumbnail URL by using the cfg.db.UpdateVideo function. The thumbnail URL should have this format:
	// http://localhost:<port>/api/thumbnails/{videoID}
	thumbnailURL := fmt.Sprintf("http://localhost:%s/api/thumbnails/%s", cfg.port, videoID)
	video.ThumbnailURL = &thumbnailURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video", err)
		return
	}

	// 8. Respond with updated JSON of the video's metadata. Use the provided respondWithJSON function and pass it the updated database.Video struct to marshal.
	respondWithJSON(w, http.StatusOK, video)
}
