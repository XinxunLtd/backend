package users

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"project/database"
	"project/models"
	"project/utils"
)

// PUT /v3/users/profile
func UpdateProfileHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	var err error
	err = r.ParseMultipartForm(5 << 20) // 5MB
	if err != nil {
		utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Invalid form data"})
		return
	}

	db := database.DB
	var user models.User
	if err := db.First(&user, uid).Error; err != nil {
		utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "User not found"})
		return
	}

	// Update name if provided
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" && name != "null" {
		user.Name = name
	}

	// Handle profile image upload
	// Check if profile is explicitly set to null (don't update)
	profileValue := strings.TrimSpace(r.FormValue("profile"))
	if profileValue == "null" {
		// Profile is set to null, don't update profile field
		// Just save name if it was updated
		if err := db.Save(&user).Error; err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal menyimpan data"})
			return
		}

		// Build response
		responseData := map[string]interface{}{
			"name": user.Name,
		}
		if user.Profile != nil {
			responseData["profile"] = *user.Profile
		} else {
			responseData["profile"] = nil
		}

		utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
			Success: true,
			Message: "Profile berhasil diperbarui",
			Data:    responseData,
		})
		return
	}

	// If profile is not null, check if file is uploaded
	file, handler, err := r.FormFile("profile")
	if err == nil && handler != nil {
		defer file.Close()

		// Validate file extension
		ext := strings.ToLower(filepath.Ext(handler.Filename))
		allowedExts := map[string]bool{
			".jpg":  true,
			".jpeg": true,
			".png":  true,
			".heic": true,
			".heif": true,
			".webp": true,
		}
		if !allowedExts[ext] {
			utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar harus JPG/PNG/HEIC/HEIF/WEBP"})
			return
		}

		// Validate file size
		if handler.Size > 5<<20 {
			utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar maksimal 5MB"})
			return
		}

		// Read first 512 bytes to detect MIME type
		buf := make([]byte, 512)
		n, err := file.Read(buf)
		if err != nil && err != http.ErrBodyReadAfterClose && err != io.EOF {
			utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gagal membaca gambar"})
			return
		}
		detected := http.DetectContentType(buf[:n])

		// Check if it's HEIC/HEIF/WEBP format
		isHEIC := ext == ".heic" || ext == ".heif" || detected == "image/heic" || detected == "image/heif"
		isWEBP := ext == ".webp" || detected == "image/webp"

		var imageBytes []byte
		var imageSize int64

		if isHEIC || isWEBP {
			// For HEIC/HEIF/WEBP, upload directly without decode/encode
			if _, err := file.Seek(0, 0); err != nil {
				utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gagal membaca gambar"})
				return
			}
			imageBytes, err = io.ReadAll(file)
			if err != nil {
				utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gagal membaca gambar"})
				return
			}
			imageSize = int64(len(imageBytes))
		} else {
			// For JPG/PNG, validate MIME type and decode/encode to sanitize
			if detected != "image/jpeg" && detected != "image/png" {
				utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar harus JPG/PNG/HEIC/HEIF/WEBP"})
				return
			}

			// Rewind and read all bytes
			if _, err := file.Seek(0, 0); err != nil {
				utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gagal membaca gambar"})
				return
			}
			allBytes, err := io.ReadAll(file)
			if err != nil {
				utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gagal membaca gambar"})
				return
			}

			// Decode and re-encode to sanitize
			imgReader := bytes.NewReader(allBytes)
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
				utils.WriteJSON(w, http.StatusBadRequest, utils.APIResponse{Success: false, Message: "Gambar harus JPG/PNG/HEIC/HEIF/WEBP"})
				return
			}
			imageBytes = outBuf.Bytes()
			imageSize = int64(len(imageBytes))
			// Use .jpg extension for sanitized images
			if ext == ".jpeg" {
				ext = ".jpg"
			}
		}

		// Delete old profile image from S3 if exists
		if user.Profile != nil && *user.Profile != "" {
			_ = utils.DeleteFromS3(*user.Profile)
		}

		// Generate new profile image name
		randomNum := time.Now().UnixNano()
		imgName := "profile_" + strconv.FormatUint(uint64(uid), 10) + "_" + strconv.FormatInt(randomNum, 10) + ext

		// Upload to S3
		reader := bytes.NewReader(imageBytes)
		if err := utils.UploadToS3(imgName, reader, imageSize); err != nil {
			utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal mengupload gambar"})
			return
		}

		// Update user profile
		user.Profile = &imgName
	}

	// Save user
	if err := db.Save(&user).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal menyimpan data"})
		return
	}

	// Build response
	responseData := map[string]interface{}{
		"name": user.Name,
	}
	if user.Profile != nil {
		responseData["profile"] = *user.Profile
	} else {
		responseData["profile"] = nil
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Profile berhasil diperbarui",
		Data:    responseData,
	})
}

// DELETE /v3/users/profile
func DeleteProfileHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := utils.GetUserID(r)
	if !ok || uid == 0 {
		utils.WriteJSON(w, http.StatusUnauthorized, utils.APIResponse{Success: false, Message: "Unauthorized"})
		return
	}

	db := database.DB
	var user models.User
	if err := db.First(&user, uid).Error; err != nil {
		utils.WriteJSON(w, http.StatusNotFound, utils.APIResponse{Success: false, Message: "User not found"})
		return
	}

	// Delete profile image from S3 if exists
	if user.Profile != nil && *user.Profile != "" {
		if err := utils.DeleteFromS3(*user.Profile); err != nil {
			// Log error but continue (file might not exist)
			_ = err
		}
	}

	// Clear profile in database
	user.Profile = nil
	if err := db.Save(&user).Error; err != nil {
		utils.WriteJSON(w, http.StatusInternalServerError, utils.APIResponse{Success: false, Message: "Gagal menghapus profile"})
		return
	}

	// Build response
	responseData := map[string]interface{}{
		"name":    user.Name,
		"profile": nil,
	}

	utils.WriteJSON(w, http.StatusOK, utils.APIResponse{
		Success: true,
		Message: "Profile berhasil dihapus",
		Data:    responseData,
	})
}

