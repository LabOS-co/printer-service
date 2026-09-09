package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
)

var iv = []byte("0123456789ABCDEF")

func EncryptAES(data string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	paddedData := padPKCS7([]byte(data), aes.BlockSize)
	ciphertext := make([]byte, len(paddedData))

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, paddedData)

	encodedEncryptedData := base64.StdEncoding.EncodeToString(ciphertext)
	return encodedEncryptedData, nil
}

func DecryptAES(ciphertext string, key []byte) (string, error) {
	decodedEncryptedData, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	plaintext := make([]byte, len(decodedEncryptedData))

	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(plaintext, decodedEncryptedData)

	unpaddedData, err := unpadPKCS7(plaintext)
	if err != nil {
		return "", err
	}

	return string(unpaddedData), nil
}

func padPKCS7(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	pad := byte(padding)
	for i := 0; i < padding; i++ {
		data = append(data, pad)
	}
	return data
}

func unpadPKCS7(data []byte) ([]byte, error) {
	padding := int(data[len(data)-1])
	if padding > aes.BlockSize || padding == 0 {
		return nil, fmt.Errorf("invalid padding")
	}
	return data[:len(data)-padding], nil
}
