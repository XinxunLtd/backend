package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const FazpassBaseURL = "https://api.fazpass.com"

// FazpassError represents a Fazpass API error
type FazpassError struct {
	Code     string
	Message  string
	HTTPCode int
}

func (e *FazpassError) Error() string {
	return fmt.Sprintf("fazpass error [%s]: %s", e.Code, e.Message)
}

// GetUserFriendlyMessage returns a user-friendly error message based on Fazpass error code
func GetUserFriendlyMessage(code string) string {
	switch code {
	// HTTP 400
	case "4000201":
		return "Gateway tidak ditemukan. Silakan hubungi administrator."
	case "4000205":
		return "Merchant tidak aktif. Silakan hubungi administrator."
	case "4000206":
		return "Gateway tidak aktif. Silakan hubungi administrator."
	case "4000207":
		return "Kredensial tidak tersedia. Silakan hubungi administrator."

	// HTTP 401
	case "4010207":
		return "Identitas pengguna tidak tersedia."
	case "4010208":
		return "Terlalu banyak permintaan. Silakan coba lagi nanti."

	// HTTP 402
	case "4020201":
		return "Saldo tidak mencukupi. Silakan hubungi administrator."

	// HTTP 403
	case "4030201":
		return "Kode OTP tidak valid."
	case "4030202":
		return "Kode OTP sudah kadaluarsa."
	case "4030203":
		return "Kode OTP sudah pernah digunakan."

	// HTTP 404
	case "4040201":
		return "Transaksi tidak ditemukan."
	case "4040202":
		return "Inisiasi tidak ditemukan."

	// HTTP 405
	case "4050201":
		return "Provider tidak ditemukan."
	case "4050202":
		return "Tipe OTP tidak ditemukan."
	case "4050203":
		return "Harga tidak ditemukan."
	case "4050204":
		return "Operator tidak ditemukan."

	// HTTP 422
	case "4220201":
		return "Data yang dikirim tidak valid."
	case "4220202":
		return "Anda harus memasukkan OTP atau URL."
	case "4220205", "4220206":
		return "Nomor telepon yang Anda masukkan tidak valid."
	case "4220207":
		return "Anda tidak diizinkan mengirim untuk provider ini."
	case "4220208":
		return "Parameter tidak lengkap atau format tidak benar."
	case "4220209":
		return "OTP yang Anda masukkan tidak valid."
	case "4220210":
		return "Operator tidak didukung."

	// HTTP 429
	case "4290201":
		return "Anda telah gagal memverifikasi nomor. Silakan coba lagi dalam 24 jam."
	case "4290202":
		return "Kuota POC Anda telah habis. Silakan hubungi account manager."

	// HTTP 500
	case "5000200":
		return "Terjadi kesalahan pada server. Silakan coba lagi nanti."
	case "5000202":
		return "Gagal membuat record. Silakan coba lagi nanti."

	// HTTP 501
	case "5010201":
		return "OTP tidak diizinkan."

	// HTTP 503
	case "5030201":
		return "Layanan pihak ketiga tidak tersedia. Silakan coba lagi nanti."
	case "5030202":
		return "Gagal memparse data respons."
	case "5030204":
		return "Base URL kosong."
	case "5030205":
		return "Gagal memparse URL."
	case "5030206":
		return "Vendor sedang down."
	case "5030207":
		return "Operator untuk channel ini sedang down."
	case "5030200":
		return "Channel error: URL provider tidak valid."

	default:
		// Check by HTTP status code prefix
		if strings.HasPrefix(code, "400") {
			return "Permintaan tidak valid. Silakan periksa data yang dikirim."
		}
		if strings.HasPrefix(code, "401") {
			return "Tidak diizinkan. Silakan periksa kredensial Anda."
		}
		if strings.HasPrefix(code, "402") {
			return "Pembayaran diperlukan."
		}
		if strings.HasPrefix(code, "403") {
			return "Akses ditolak."
		}
		if strings.HasPrefix(code, "404") {
			return "Data tidak ditemukan."
		}
		if strings.HasPrefix(code, "405") {
			return "Metode tidak diizinkan."
		}
		if strings.HasPrefix(code, "422") {
			return "Data yang dikirim tidak valid."
		}
		if strings.HasPrefix(code, "429") {
			return "Terlalu banyak permintaan. Silakan coba lagi nanti."
		}
		if strings.HasPrefix(code, "500") {
			return "Terjadi kesalahan pada server. Silakan coba lagi nanti."
		}
		if strings.HasPrefix(code, "501") {
			return "Fitur tidak didukung."
		}
		if strings.HasPrefix(code, "503") {
			return "Layanan tidak tersedia. Silakan coba lagi nanti."
		}
		return "Terjadi kesalahan. Silakan coba lagi nanti."
	}
}

type FazpassOTPRequest struct {
	Phone      string `json:"phone"`
	GatewayKey string `json:"gateway_key"`
}

type FazpassOTPResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Code    string `json:"code"`
	Data    struct {
		ID        string `json:"id"`
		OTP       string `json:"otp"`
		OTPLength int    `json:"otp_length"`
		Channel   string `json:"channel"`
		Provider  string `json:"provider"`
		Purpose   string `json:"purpose"`
	} `json:"data"`
}

type FazpassOTPVerifyRequest struct {
	OTPID string `json:"otp_id"`
	OTP   string `json:"otp"`
}

type FazpassOTPVerifyResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// RequestOTP requests OTP from Fazpass API
func RequestOTP(phone string) (*FazpassOTPResponse, error) {
	merchantKey := os.Getenv("FAZPASS_MERCHANT_KEY")
	gatewayKey := os.Getenv("FAZPASS_GATEWAY_KEY")

	if merchantKey == "" || gatewayKey == "" {
		return nil, fmt.Errorf("FAZPASS_MERCHANT_KEY or FAZPASS_GATEWAY_KEY not set")
	}

	// Convert phone from 8xxxx to 62xxxx
	// Phone format: 8123456789 -> 628123456789
	phoneIntl := "62" + phone

	reqBody := FazpassOTPRequest{
		Phone:      phoneIntl,
		GatewayKey: gatewayKey,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", FazpassBaseURL+"/v1/otp/request", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+merchantKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var fazpassResp FazpassOTPResponse
	if err := json.Unmarshal(body, &fazpassResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if !fazpassResp.Status {
		return nil, &FazpassError{
			Code:     fazpassResp.Code,
			Message:  fazpassResp.Message,
			HTTPCode: resp.StatusCode,
		}
	}

	return &fazpassResp, nil
}

// VerifyOTP verifies OTP with Fazpass API
func VerifyOTP(otpID, otp string) (*FazpassOTPVerifyResponse, error) {
	merchantKey := os.Getenv("FAZPASS_MERCHANT_KEY")

	if merchantKey == "" {
		return nil, fmt.Errorf("FAZPASS_MERCHANT_KEY not set")
	}

	reqBody := FazpassOTPVerifyRequest{
		OTPID: otpID,
		OTP:   otp,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", FazpassBaseURL+"/v1/otp/verify", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+merchantKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var fazpassResp FazpassOTPVerifyResponse
	if err := json.Unmarshal(body, &fazpassResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if !fazpassResp.Status {
		return nil, &FazpassError{
			Code:     fazpassResp.Code,
			Message:  fazpassResp.Message,
			HTTPCode: resp.StatusCode,
		}
	}

	return &fazpassResp, nil
}
