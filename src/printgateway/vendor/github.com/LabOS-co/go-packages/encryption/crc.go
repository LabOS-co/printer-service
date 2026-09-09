package encryption

import (
	"fmt"
	"hash/crc32"
)

func GenerateCRC32Hash(value string) string {
	hash := crc32.ChecksumIEEE([]byte(value))
	return fmt.Sprintf("%x", hash)
}
