package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

// Do not change !!
var bytes = []byte{35, 46, 57, 24, 85, 35, 24, 74, 87, 35, 88, 98, 66, 32, 14, 05}

// Do not change !!
const secret string = "ss&gg*~#^4^#s7^=)^^7!134"

type ChecksumEncryptMode string

const (
	SHA256 ChecksumEncryptMode = "sha256"
	SHA512 ChecksumEncryptMode = "sha512"
)

func Encrypt(text string) (string, error) {
	block, err := aes.NewCipher([]byte(secret))

	if err != nil {
		return "", err
	}

	plainText := []byte(text)
	cfb := cipher.NewCFBEncrypter(block, bytes)
	cipherText := make([]byte, len(plainText))
	cfb.XORKeyStream(cipherText, plainText)

	return encode(cipherText), nil
}

func Decrypt(text string) (string, error) {
	block, err := aes.NewCipher([]byte(secret))

	if err != nil {
		return "", err
	}

	cipherText, err := decode(text)

	if err != nil {
		return "", err
	}

	cfb := cipher.NewCFBDecrypter(block, bytes)
	plainText := make([]byte, len(cipherText))
	cfb.XORKeyStream(plainText, cipherText)

	return string(plainText), nil
}

func encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func decode(s string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(s)

	if err != nil {
		return nil, err
	}

	return data, nil
}

func GenerateFileChecksum(filePath string, mode ChecksumEncryptMode) (string, error) {
	// Get the absolute destination path
	// f.e \Repos\go-packages to \\Repos\\go-packages
	filePath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var hash hash.Hash

	switch mode {
	case SHA256:
		hash = sha256.New()
	case SHA512:
		fallthrough
	default:
		hash = sha512.New()
	}

	_, err = io.Copy(hash, file)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
