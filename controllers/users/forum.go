package users

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"project/database"
	"project/models"
	"project/utils"
)

// GET /api/users/forum
func ForumListHandler(w http.ResponseWriter, r *http.Request) {
	// Fungsi sensor nomor telepon
	maskNumber := func(num string) string {
		if len(num) <= 8 {
			return num
		}
		prefix := num[:3]
		suffix := num[len(num)-4:]
		masked := strings.Repeat("*", len(num)-6)
		return prefix + masked + suffix
	}
	db := database.DB

	// Get query parameters
	pageStr := r.URL.Query().Get("page")
	limitStr := r.URL.Query().Get("limit")

	// Parse pagination with defaults
	page, _ := strconv.Atoi(pageStr)
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(limitStr)
	if limit < 1 {
		limit = 10
	}

	// Count total rows
	var totalRows int64
	if err := db.Model(&models.Forum{}).Where("status = ?", "Accepted").Count(&totalRows).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "DB error"})
		return
	}

	// Calculate pagination
	totalPages := int(math.Ceil(float64(totalRows) / float64(limit)))
	offset := (page - 1) * limit

	// Query forums with pagination
	var forums []models.Forum
	if err := db.Where("status = ?", "Accepted").Order("created_at DESC").Limit(limit).Offset(offset).Find(&forums).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "DB error"})
		return
	}

	// Ambil user_id unik
	userIDs := make(map[uint]struct{})
	for _, f := range forums {
		userIDs[f.UserID] = struct{}{}
	}
	ids := make([]uint, 0, len(userIDs))
	for id := range userIDs {
		ids = append(ids, id)
	}

	// Query user names and numbers
	var users []models.User
	db.Select("id", "name", "number").Where("id IN ?", ids).Find(&users)
	type userInfo struct {
		Name   string
		Number string
	}
	userMap := make(map[uint]userInfo)
	for _, u := range users {
		userMap[u.ID] = userInfo{u.Name, u.Number}
	}

	// Build response
	type forumResp struct {
		ID          uint    `json:"id"`
		Name        string  `json:"name"`
		Number      string  `json:"number"`
		Reward      float64 `json:"reward"`
		Description string  `json:"description"`
		Image       string  `json:"image"`
		Status      string  `json:"status"`
		Time        string  `json:"time"`
	}
	resp := make([]forumResp, 0, len(forums))
	for _, f := range forums {
		u := userMap[f.UserID]
		resp = append(resp, forumResp{
			ID:          f.ID,
			Name:        u.Name,
			Number:      maskNumber(u.Number),
			Reward:      f.Reward,
			Description: f.Description,
			Image:       f.Image,
			Status:      f.Status,
			Time:        f.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	// Build response with pagination
	responseData := map[string]interface{}{
		"data": resp,
		"pagination": map[string]interface{}{
			"page":       page,
			"limit":      limit,
			"total_rows": totalRows,
			"total_pages": totalPages,
		},
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Successfully", Data: responseData})
}

// GET /api/users/check-forum //check if user has withdrawal in last 3 days and give response data {has_withdrawal: true/false}
func CheckWithdrawalForumHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	var count int64
	threeDaysAgo := time.Now().AddDate(0, 0, -3)
	db := database.DB
	db.Model(&models.Withdrawal{}).Where("user_id = ? AND created_at >= ?", uid, threeDaysAgo).Count(&count)

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{Success: true, Message: "Successfully", Data: map[string]bool{"has_withdrawal": count > 0}})
}

// POST /api/users/forum/submit
func ForumSubmitHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	var err error
	err = r.ParseMultipartForm(2 << 20) // 2MB
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Invalid form data"})
		return
	}
	description := r.FormValue("description")
	if len(description) < 5 || len(description) > 60 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Deskripsi harus 5-60 karakter"})
		return
	}
	file, handler, err := r.FormFile("image")
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar diperlukan"})
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(handler.Filename))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar harus JPG/PNG"})
		return
	}
	if handler.Size > 2<<20 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar maksimal 2MB"})
		return
	}

	// Read first 512 bytes to detect MIME type (magic-bytes)
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != http.ErrBodyReadAfterClose && err != io.EOF {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gagal membaca gambar"})
		return
	}
	detected := http.DetectContentType(buf[:n])
	if detected != "image/jpeg" && detected != "image/png" {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar harus JPG/PNG"})
		return
	}

	// Rewind and decode/re-encode image to sanitize content
	// Need the full image bytes: combine the head we read with the rest
	rest, err := io.ReadAll(file)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gagal membaca gambar"})
		return
	}
	imageBytes := append(buf[:n], rest...)

	// Placeholder: perform malware scan here (e.g., send imageBytes to ClamAV or cloud scanner)
	// If scan fails, return an error. For now we only leave a comment and proceed.

	// Decode and re-encode to sanitize metadata and ensure a valid image
	imgReader := bytes.NewReader(imageBytes)
	img, format, err := image.Decode(imgReader)
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Invalid image format"})
		return
	}

	var outBuf bytes.Buffer
	switch format {
	case "jpeg":
		if err := jpeg.Encode(&outBuf, img, &jpeg.Options{Quality: 85}); err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal memproses gambar"})
			return
		}
	case "png":
		if err := png.Encode(&outBuf, img); err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal memproses gambar"})
			return
		}
	default:
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar harus JPG/PNG"})
		return
	}

	// Prepare a ReadSeeker for S3 upload and presign
	reader := bytes.NewReader(outBuf.Bytes())

	// Check withdrawal in last 3 days
	var count int64
	threeDaysAgo := time.Now().AddDate(0, 0, -3)
	db := database.DB
	db.Model(&models.Withdrawal{}).Where("user_id = ? AND created_at >= ?", uid, threeDaysAgo).Count(&count)
	if count == 0 {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Tidak ada penarikan dalam 3 hari terakhir"})
		return
	}
	// Upload image to S3 (private) and get presigned URL
	randomNum := time.Now().UnixNano()
	uidUint := uid
	imgName := strconv.FormatUint(uint64(uidUint), 10) + "_" + strconv.FormatInt(randomNum, 10) + ext
	// use UploadToS3AndPresign which expects a ReadSeeker and returns a presigned URL
	presignedURL, upErr := utils.UploadToS3AndPresign(imgName, reader, int64(outBuf.Len()), 3600)
	if upErr != nil {
		_ = upErr
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Failed to upload image. Please try again later."})
		return
	}
	_ = presignedURL // currently not stored; consumed by clients via separate endpoint if needed

	forum := models.Forum{
		UserID:      uidUint,
		Description: description,
		Image:       imgName,
		Status:      "Pending",
	}
	if err := db.Create(&forum).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "DB error"})
		return
	}
	utils.WriteJSON(w, http.StatusCreated, utils.APIResponse{Success: true, Message: "Postingangan terkirim, menunggu persetujuan."})
}
